package lenses

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kataras/golog"
)

// ResponseType is the corresponding message type for the response came from the back-end server to the client.
type ResponseType string

const (
	// WildcardResponse is a custom type only for the go library
	// which can be passed to the `On` event in order to catch all the incoming messages and fire the corresponding callback response handler.
	WildcardResponse ResponseType = "*"
	// ErrorResponse is the "ERROR" receive message type.
	ErrorResponse ResponseType = "ERROR"
	// InvalidRequestResponse is the "INVALIDREQUEST" receive message type.
	InvalidRequestResponse ResponseType = "INVALIDREQUEST"
	// RecordMessageResponse is the "RECORD" receive message type.
	RecordMessageResponse ResponseType = "RECORD"
	// HeartbeatResponse is the "HEARTBEAT" receive message type.
	HeartbeatResponse ResponseType = "HEARTBEAT"
	// SuccessResponse is the "SUCCESS" receive message type.
	SuccessResponse ResponseType = "SUCCESS"
	// StatsResponse in the "STATS" receive message type
	StatsResponse ResponseType = "STATS"
	// EndResponse is the "END" receive message type for browsing
	EndResponse ResponseType = "END"
)

type (
	MetaData struct {
		Timestamp int `json:"timestamp"`
		KeySize   int `json:"__keysize"`
		ValueSize int `json:"__valuesize"`
		Partition int `json:"partition"`
		Offset    int `json:"offset"`
	}

	Data struct {
		Key      json.RawMessage `json:"key"`
		Value    json.RawMessage `json:"value"`
		Metadata MetaData        `json:"metadata"`
		RowNum   int             `json:"rownum"`
	}

	// LiveResponse contains the necessary information that
	// the websocket client expects to receive from the back-end websocket server.
	LiveResponse struct {
		// Type describes what response content the client has
		// received. Available values are: "ERROR",
		Type ResponseType `json:"type"`

		// Content contains the actual response content.
		// Each response type has its own content layout.
		Data Data `json:"data"`
	}
)

type (
	// LiveConfiguration contains the contact information
	// about the websocket communication.
	// It contains the host(including the scheme),
	// the user and password credentials
	// and, optionally, the client id which is the kafka consumer group.
	//
	// See `OpenLiveConnection` for more.
	LiveConfiguration struct {
		Host  string `json:"host"`
		Token string `json:"token"`
		Debug bool   `json:"debug"`
		SQL   string `json:"sql"`
		Live  bool   `json:"live"`
		Stats int    `json:"stats"`
		// ws-specific settings, optionally.

		// HandshakeTimeout specifies the duration for the handshake to complete.
		HandshakeTimeout time.Duration
		// ReadBufferSize and WriteBufferSize specify I/O buffer sizes. If a buffer
		// size is zero, then a useful default size is used. The I/O buffer sizes
		// do not limit the size of the messages that can be sent or received.
		ReadBufferSize, WriteBufferSize int

		// TLSClientConfig specifies the TLS configuration to use with tls.Client.
		// If nil, the default configuration is used.
		TLSClientConfig *tls.Config
	}

	// LiveConnection is the websocket connection.
	LiveConnection struct {
		conn   *websocket.Conn
		config LiveConfiguration

		receiveStop chan struct{}
		closed      uint32

		authToken string // generated by the login and `OnSuccess` internal listener.
		endpoint  string // generated by the config's host and the client id.

		listeners map[ResponseType][]LiveListener
		mu        sync.RWMutex

		errors chan error // error comes from reader.
	}
)

// OpenLiveConnection starts the websocket communication
// and returns the client connection for further operations.
// An error will be returned if login failed.
//
// The `Err` function is used to report any
// reader's error, the reader operates on its own go routine.
//
// The connection starts reading immediately, the implementation is subscribed to the `Success` message
// to validate the login.
//
// Usage:
// c, err := lenses.OpenLiveConnection(lenses.LiveConfiguration{
//    [...]
// })
//
// c.On(lenses.KafkaMessageResponse, func(pub lenses.LivePublisher, response lenses.LiveResponse) error {
//    [...]
// })
//
// c.On(lenses.WildcardResponse, func(pub lenses.LivePublisher, response lenses.LiveResponse) error {
//    [...catch all messages]
// })
//
// c.OnSuccess(func(cub lenses.LivePublisher, response lenses.LiveResponse) error{
//    pub.Publish(lenses.SubscribeRequest, 2, `{"sqls": ["SELECT * FROM reddit_posts LIMIT 3"]}`)
// }) also OnKafkaMessage, OnError, OnHeartbeat, OnInvalidRequest.
//
// If at least one listener returned an error then the communication is terminated.
func OpenLiveConnection(config LiveConfiguration) (*LiveConnection, error) {
	if config.Debug {
		golog.SetLevel("debug")
	}

	if config.HandshakeTimeout == 0 {
		config.HandshakeTimeout = 45 * time.Second
	}

	config.Host = strings.Replace(config.Host, "https://", "wss://", 1)
	config.Host = strings.Replace(config.Host, "https://", "ws://", 1)

	//ws://localhost:24015/api/ws/v1/sql/execute?sql=
	query := url.QueryEscape(config.SQL)
	endpoint := fmt.Sprintf("%s/api/ws/v1/sql/execute?sql=%s&token=%s", config.Host, query, config.Token)

	if config.Live {
		endpoint = fmt.Sprintf("%s/api/ws/v1/sql/execute?sql=%s&token=%s&live=true", config.Host, query, config.Token)
	}

	c := &LiveConnection{
		config:      config,
		endpoint:    endpoint,
		receiveStop: make(chan struct{}),
		listeners:   make(map[ResponseType][]LiveListener),
		errors:      make(chan error),
	}

	return c, c.start()
}

func (c *LiveConnection) start() error {
	// first connect, handshake with the websocket server for upgrade.
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: c.config.HandshakeTimeout,
		ReadBufferSize:   c.config.ReadBufferSize,
		WriteBufferSize:  c.config.WriteBufferSize,
	}

	conn, _, err := dialer.Dial(c.endpoint, nil)

	if err != nil {
		err = fmt.Errorf("connect failure for [%s]: %v", c.config.Host, err)
		golog.Debug(err)
		return err
	}
	// set the websocket connection.
	c.conn = conn

	go c.readLoop()
	return nil
}

// Wait waits until interruptSignal fires, if it's nil then it waits for ever.
func (c *LiveConnection) Wait(interruptSignal <-chan os.Signal) error {
	select {
	case <-interruptSignal:
		return c.Close()
	}
}

// type userPayload struct {
// 	User     string `json:"user"`
// 	Password string `json:"password"`
// }

// Err can be used to receive the errors coming from the communication,
// the listeners' errors are sending to that channel too.
func (c *LiveConnection) Err() <-chan error {
	return c.errors
}

func (c *LiveConnection) sendErr(err error) {
	golog.Debug(err)
	c.errors <- err
}

func (c *LiveConnection) readLoop() {
	defer c.Close() // close on any errors or loop break.
	for {
		select {
		case <-c.receiveStop:
			golog.Debugf("stop receiving by signal")
			return
		default:
			resp := LiveResponse{}
			if err := c.conn.ReadJSON(&resp); err != nil {
				if _, is := err.(*net.OpError); is {
					// send it as it's and do not exit, caller may want to check if should manage that error or just ignore it.
					// caused by manual interruption(ctrl/cmd+c) or real network issue(this is why we continue after the error here).
					c.sendErr(err)
					continue
				}
				c.sendErr(fmt.Errorf("live: read json: [%v]", err))
				continue
			}

			golog.Debugf("read: [%#+v]", resp)

			// fire.
			c.mu.RLock()
			callbacks, ok := c.listeners[resp.Type]
			c.mu.RUnlock()

			if ok {
				for _, cb := range callbacks {
					if err := cb(resp); err != nil {
						// return err // break and exit the loop on first failure.
						c.sendErr(err) // don't break, just add the error.
					}
				}
			}
		}
	}
}

// --- Events handles incoming messages with style. ---

// LiveListener is the declaration for the subscriber, the subscriber
// is just a callback which fires whenever a websocket message
// with a particular `ResponseType` was sent by the websocket server.
//
// See `On` too.
type LiveListener func(LiveResponse) error

// On adds a listener, a websocket message subscriber based on the given "typ" `ResponseType`.
// Use the `WildcardResponse` to subscribe to all message types.
func (c *LiveConnection) On(typ ResponseType, cb LiveListener) {
	if typ == WildcardResponse {
		c.OnError(cb)
		c.OnInvalidRequest(cb)
		c.OnRecordMessage(cb)
		c.OnHeartbeat(cb)
		c.OnSuccess(cb)
		c.OnStats(cb)
		c.OnEnd(cb)
		return
	}

	c.mu.Lock()
	c.listeners[typ] = append(c.listeners[typ], cb)
	c.mu.Unlock()
}

// OnError adds a listener, a websocket message subscriber based on the "ERROR" `ResponseType`.
func (c *LiveConnection) OnError(cb LiveListener) { c.On(ErrorResponse, cb) }

// OnInvalidRequest adds a listener, a websocket message subscriber based on the "INVALIDREQUEST" `ResponseType`.
func (c *LiveConnection) OnInvalidRequest(cb LiveListener) { c.On(InvalidRequestResponse, cb) }

// OnKafkaMessage adds a listener, a websocket message subscriber based on the "RECORD" `ResponseType`.
func (c *LiveConnection) OnRecordMessage(cb LiveListener) { c.On(RecordMessageResponse, cb) }

// OnHeartbeat adds a listener, a websocket message subscriber based on the "HEARTBEAT" `ResponseType`.
func (c *LiveConnection) OnHeartbeat(cb LiveListener) { c.On(HeartbeatResponse, cb) }

// OnSuccess adds a listener, a websocket message subscriber based on the "SUCCESS" `ResponseType`.
func (c *LiveConnection) OnSuccess(cb LiveListener) { c.On(SuccessResponse, cb) }

// OnStats adds a listener, a websocket message subscriber based on the "STATS" `ResponseType`.
func (c *LiveConnection) OnStats(cb LiveListener) { c.On(StatsResponse, cb) }

// OnEnd adds a listener, a websocket message subscriber based on the "END" `ResponseType`.
func (c *LiveConnection) OnEnd(cb LiveListener) { c.On(EndResponse, cb) }

// Close closes the underline websocket connection
// and stops receiving any new message from the websocket server.
//
// If `Close` called more than once then it will return nil and nothing will happen.
func (c *LiveConnection) Close() error {
	golog.Debugf("terminating websocket connection...")
	// if we try to close a closed channel panic will occur,
	// in order to prevent it we've added an atomic checkpoint.
	if atomic.LoadUint32(&c.closed) > 0 {
		// means already closed.
		return nil
	}

	atomic.StoreUint32(&c.closed, 1)
	close(c.receiveStop) // stop receiving, see `readLoop`.
	return c.conn.Close()
}

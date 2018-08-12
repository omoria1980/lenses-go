package main

import (
	"github.com/landoop/lenses-go"

	"github.com/landoop/bite"
	"github.com/spf13/cobra"
)

func init() {
	app.AddCommand(newGetQuotasCommand())
	app.AddCommand(newQuotaGroupCommand())
}

func newGetQuotasCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:              "quotas",
		Short:            "List of all available quotas",
		Example:          "quotas",
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			quotas, err := client.GetQuotas()
			if err != nil {
				return err
			}

			return bite.PrintObject(cmd, quotas)
		},
	}

	bite.CanPrintJSON(cmd)

	return cmd
}

func newQuotaGroupCommand() *cobra.Command {
	root := &cobra.Command{
		Use:              "quota",
		Short:            "Work with particular a quota, create a new quota or update and delete an existing one",
		Example:          `quota users set [--quota-user=""] [--quota-client=""] --quota-config="{\"producer_byte_rate\": \"100000\",\"consumer_byte_rate\": \"200000\",\"request_percentage\": \"75\"}"`,
		TraverseChildren: true,
		SilenceErrors:    true,
	}

	root.AddCommand(newQuotaUsersSubGroupCommand())
	root.AddCommand(newQuotaClientsSubGroupCommand())

	return root
}

type createQuotaPayload struct {
	Config lenses.QuotaConfig `yaml:"Config"`
	// for specific user and/or client.
	User string `yaml:"User"`
	// if "all" or "*" then means all clients.
	// Minor note On quota clients set/create/update the Config and Client field are used only.
	ClientID string `yaml:"Client"`
}

func newQuotaUsersSubGroupCommand() *cobra.Command {
	var (
		configRaw string
		quota     createQuotaPayload
	)

	rootSub := &cobra.Command{
		Use:              "users",
		Short:            "Work with users quotas",
		Example:          "quota users",
		TraverseChildren: true,
		SilenceErrors:    true,
	}

	setCommand := &cobra.Command{
		Use:              "set",
		Aliases:          []string{"create", "update"},
		Short:            "Create or update the default user quota or a specific user quota (and/or client(s))",
		Example:          `quota users set [--quota-user="user"] [--quota-client=""] --quota-config="{\"producer_byte_rate\": \"100000\",\"consumer_byte_rate\": \"200000\",\"request_percentage\": \"75\"}"`,
		TraverseChildren: true,
		SilenceErrors:    true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := bite.CheckRequiredFlags(cmd, bite.FlagPair{"quota-config": configRaw}); err != nil {
				return err
			}

			// bite.FriendlyError(cmd, errResourceNotAccessibleMessage, "unable to access quota, user has no rights for this action")

			if quota.User != "" {
				if clientID := quota.ClientID; clientID != "" {
					if clientID == "all" || clientID == "*" {
						if err := client.CreateOrUpdateQuotaForUserAllClients(quota.User, quota.Config); err != nil {
							return err
						}

						return bite.PrintInfo(cmd, "Quota for user %s and all clients set", quota.User)

					}

					if err := client.CreateOrUpdateQuotaForUserClient(quota.User, clientID, quota.Config); err != nil {
						return err
					}

					return bite.PrintInfo(cmd, "Quota for user %s and client %s set", quota.User, clientID)
				}

				if err := client.CreateOrUpdateQuotaForUser(quota.User, quota.Config); err != nil {
					return err
				}

				return bite.PrintInfo(cmd, "Quota for user %s created/updated", quota.User)
			}

			if err := client.CreateOrUpdateQuotaForAllUsers(quota.Config); err != nil {
				return err
			}

			return bite.PrintInfo(cmd, "Default user quota created/updated")
		},
	}

	setCommand.Flags().StringVar(&configRaw, "quota-config", "", `--quota-config="{\"key\": \"value\"}"`)
	setCommand.Flags().StringVar(&quota.User, "quota-user", "", "--quota-user=")
	setCommand.Flags().StringVar(&quota.ClientID, "quota-client", "", "--quota-client=")

	bite.CanBeSilent(setCommand)
	bite.Prepend(setCommand, bite.FileBind(&quota, bite.ElseBind(func() error { return bite.TryReadFile(configRaw, &quota.Config) })))

	rootSub.AddCommand(setCommand)

	deleteCommand := &cobra.Command{
		Use:              "delete",
		Short:            "Delete the default user quota or a specific quota for a user (and client)",
		Example:          `quota users delete [to delete for all users] or --quota-client=* [for all clients or to a specific one] or --quota-user="user" producer_byte_rate or/and consumer_byte_rate or/and request_percentage to remove quota's config properties, if arguments empty then all keys will be passed on`,
		TraverseChildren: true,
		SilenceErrors:    true,
		RunE: func(cmd *cobra.Command, args []string) error {
			actionMsg := "delete" // +d in the echo message.
			if len(args) > 0 {
				// if arguments are not empty then it should show "update(d)",
				// otherwise it's a deletion because it deletes the whole default user quota.
				actionMsg = "update"
			}

			// bite.FriendlyError(cmd, errResourceNotAccessibleMessage, "unable to %s quota, user has no rights for this action", actionMsg)

			var user, clientID = quota.User, quota.ClientID

			if user != "" {
				if clientID != "" {
					if clientID == "all" || clientID == "*" {
						if err := client.DeleteQuotaForUserAllClients(user, args...); err != nil {
							bite.FriendlyError(cmd, errResourceNotFoundMessage, "unable to %s, quota for user: '%s' does not exist", actionMsg, user)
							return err
						}

						return bite.PrintInfo(cmd, "Quota for user %s deleted for all clients", user)
					}

					if err := client.DeleteQuotaForUserClient(user, clientID, args...); err != nil {
						bite.FriendlyError(cmd, errResourceNotFoundMessage, "unable to %s, quota for user: '%s' and client: '%s' does not exist", actionMsg, user, clientID)
						return err
					}

					return bite.PrintInfo(cmd, "Quota for user %s deleted for client %s", user, clientID)
				}

				if err := client.DeleteQuotaForUser(user, args...); err != nil {
					bite.FriendlyError(cmd, errResourceNotFoundMessage, "unable to %s, quota for user: '%s' does not exist", actionMsg, user)
					return err
				}

				return bite.PrintInfo(cmd, "Quota for user %s %sd", user, actionMsg)
			}

			if err := client.DeleteQuotaForAllUsers(args...); err != nil {
				return err
			}

			return bite.PrintInfo(cmd, "Default user quota %sd", actionMsg)
		},
	}

	deleteCommand.Flags().StringVar(&quota.User, "quota-user", "", "--quota-user=")
	deleteCommand.Flags().StringVar(&quota.ClientID, "quota-client", "", "--quota-client=")
	bite.CanBeSilent(deleteCommand)

	rootSub.AddCommand(deleteCommand)

	return rootSub
}

func newQuotaClientsSubGroupCommand() *cobra.Command {
	var (
		configRaw string
		quota     createQuotaPayload
	)

	rootSub := &cobra.Command{
		Use:              "clients",
		Short:            "Work with clients quotas",
		Example:          "quota clients",
		TraverseChildren: true,
		SilenceErrors:    true,
	}

	setCommand := &cobra.Command{
		Use:              "set",
		Aliases:          []string{"create", "update"},
		Short:            "Create or update the default client quota or for a specific client",
		Example:          `quota clients set [--quota-client=""] --quota-config="{\"producer_byte_rate\": \"100000\",\"consumer_byte_rate\": \"200000\",\"request_percentage\": \"75\"}"`,
		TraverseChildren: true,
		SilenceErrors:    true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := bite.CheckRequiredFlags(cmd, bite.FlagPair{"quota-config": configRaw}); err != nil {
				return err
			}

			// bite.FriendlyError(cmd, errResourceNotAccessibleMessage, "unable to access quota, user has no rights for this action")

			if id := quota.ClientID; id != "" && id != "all" && id != "*" {
				if err := client.CreateOrUpdateQuotaForClient(quota.ClientID, quota.Config); err != nil {
					return err
				}

				return bite.PrintInfo(cmd, "Quota for client %s created/updated", quota.ClientID)
			}

			if err := client.CreateOrUpdateQuotaForAllClients(quota.Config); err != nil {
				return err
			}

			return bite.PrintInfo(cmd, "Default client quota created/updated")
		},
	}

	setCommand.Flags().StringVar(&configRaw, "quota-config", "", `--quota-config="{\"key\": \"value\"}"`)
	setCommand.Flags().StringVar(&quota.ClientID, "quota-client", "", "--quota-client=")
	bite.CanBeSilent(setCommand)
	bite.Prepend(setCommand, bite.FileBind(&quota, bite.ElseBind(func() error { return bite.TryReadFile(configRaw, &quota.Config) })))

	rootSub.AddCommand(setCommand)

	deleteCommand := &cobra.Command{
		Use:              "delete",
		Short:            "Delete the default client quota or a specific one",
		Example:          `quota clients delete [--quota-client=""] producer_byte_rate or/and consumer_byte_rate or/and request_percentage to remove quota's config properties, if arguments empty then all keys will be passed on`,
		TraverseChildren: true,
		SilenceErrors:    true,
		RunE: func(cmd *cobra.Command, args []string) error {
			actionMsg := "delete" // +d in the echo message.
			if len(args) > 0 {
				// if arguments are not empty then it should show "update(d)",
				// otherwise it's a deletion because it deletes the whole default user quota.
				actionMsg = "update"
			}

			// bite.FriendlyError(cmd, errResourceNotAccessibleMessage, "unable to %s quota, user has no rights for this action", actionMsg)

			if id := quota.ClientID; id != "" && id != "all" && id != "*" {
				if err := client.DeleteQuotaForClient(id, args...); err != nil {
					bite.FriendlyError(cmd, errResourceNotFoundMessage, "unable to %s, quota for client: '%s' does not exist", actionMsg, id)
					return err
				}

				return bite.PrintInfo(cmd, "Quota for client %s %sd", id, actionMsg)
			}

			if err := client.DeleteQuotaForAllClients(args...); err != nil {
				return err
			}

			return bite.PrintInfo(cmd, "Default client quota %sd", actionMsg)
		},
	}

	deleteCommand.Flags().StringVar(&quota.ClientID, "quota-client", "", "--quota-client=")
	bite.CanBeSilent(deleteCommand)

	rootSub.AddCommand(deleteCommand)

	return rootSub
}

// Package ghostfolio sends activities to a Ghostfolio instance.
//
// The client uses the REST API of Ghostfolio. It logs in with the security
// token of a user, then it uses the short-lived token from that login.
//
//	client, err := ghostfolio.NewClient("http://ghostfolio:3333", securityToken)
//	result, err := client.Import(ctx, activities, false)
package ghostfolio

// Package inzhur is a read-only client for the Inzhur cabinet API.
//
// The API has no documentation and can change at any time. The client
// refreshes a session that the user started in a browser. Then it reads the
// transaction history and the holdings. It never does the login.
//
// The client keeps no state. The caller stores the refresh token:
//
//	client := inzhur.NewClient()
//	session, err := client.Refresh(ctx, refreshToken)
//	// Save session.RefreshToken before you do anything else.
//	transactions, err := client.Transactions(ctx, session.AccessToken)
package inzhur

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/resend/resend-go/v3"
)

var resendHTTPClient = &http.Client{Timeout: 10 * time.Second}

// sendLoginToken emails the one-time login token to the given address
// asynchronously, so the caller is never blocked waiting for the API.
func sendLoginToken(email, token string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		client := resend.NewCustomClient(resendHTTPClient, cfg.ResendAPIKey)

		body := fmt.Sprintf(`<p>Your Timox login token is:</p>
<h2 style="letter-spacing:0.15em;font-family:monospace">%s</h2>
<p>It expires in 10 minutes. Do not share it.</p>`, token)

		params := &resend.SendEmailRequest{
			From:    cfg.ResendFrom,
			To:      []string{email},
			Subject: "Your Timox login token",
			Html:    body,
		}

		if _, err := client.Emails.SendWithContext(ctx, params); err != nil {
			log.Printf("[email] failed to send token to %s: %v\n", email, err)
		}
	}()
}

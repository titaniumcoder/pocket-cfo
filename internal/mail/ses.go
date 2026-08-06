// Package mail sends the email-login link via Amazon SES's SendEmail API
// (AWS SDK for Go v2) — plain text, no templates. See ARCHITECTURE.md §8.
package mail

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
)

// Config holds SES sending config. Empty From means "not configured" —
// SendLoginLink then logs the link instead of calling SES, so the
// email-login flow is testable locally without real AWS credentials
// (mirrors the ENV=prod-only enforcement of these vars in cmd/pocketcfo's
// loadConfig). Credentials themselves come from the AWS SDK's standard
// default chain (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY env vars on
// Fly.io, or an attached IAM role elsewhere) — not app-specific config, so
// they aren't part of this struct.
type Config struct {
	Region string
	From   string
}

// SendLoginLink emails toEmail their signed login link via SES. With no
// From address configured, it logs the link instead of sending — see
// Config. httpClient is reused for the SES client's outbound calls rather
// than letting the SDK build its own.
func SendLoginLink(ctx context.Context, httpClient *http.Client, cfg Config, toEmail, link string) error {
	if cfg.From == "" {
		log.Printf("mail: SES_FROM_EMAIL not set, logging login link instead of emailing %s: %s", toEmail, link)
		return nil
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region), config.WithHTTPClient(httpClient))
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}

	subject := "Your invoicer login link"
	body := fmt.Sprintf(
		"Click the link below to log in to invoicer. It expires shortly, so use it soon.\n\n%s\n\nIf you didn't request this, you can ignore this email.\n",
		link,
	)

	client := ses.NewFromConfig(awsCfg)
	_, err = client.SendEmail(ctx, &ses.SendEmailInput{
		Source:      aws.String(cfg.From),
		Destination: &types.Destination{ToAddresses: []string{toEmail}},
		Message: &types.Message{
			Subject: &types.Content{Data: aws.String(subject)},
			Body:    &types.Body{Text: &types.Content{Data: aws.String(body)}},
		},
	})
	if err != nil {
		return fmt.Errorf("ses SendEmail: %w", err)
	}
	return nil
}

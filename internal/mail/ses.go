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

type Config struct {
	Region string
	From   string
}

func SendLoginLink(ctx context.Context, httpClient *http.Client, cfg Config, toEmail, link string) error {
	if cfg.From == "" {
		log.Printf("mail: SES_FROM_EMAIL not set, logging login link instead of emailing %s: %s", toEmail, link)
		return nil
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region), config.WithHTTPClient(httpClient))
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}

	subject := "Your PocketCFO login link"
	body := fmt.Sprintf(
		"Click the link below to log in to PocketCFO. It expires shortly, so use it soon.\n\n%s\n\nIf you didn't request this, you can ignore this email.\n",
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

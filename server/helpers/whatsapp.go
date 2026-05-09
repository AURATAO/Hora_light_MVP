package helpers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

// SendWhatsAppMessage sends a plain text WhatsApp message via Meta Cloud API.
// to must include country code, e.g. "+886912345678".
func SendWhatsAppMessage(to, message string) error {
	token := os.Getenv("WHATSAPP_ACCESS_TOKEN")
	phoneNumberID := os.Getenv("WHATSAPP_PHONE_NUMBER_ID")
	if token == "" || phoneNumberID == "" {
		return fmt.Errorf("WHATSAPP_ACCESS_TOKEN or WHATSAPP_PHONE_NUMBER_ID not set")
	}

	payload := map[string]any{
		"messaging_product": "whatsapp",
		"to":                to,
		"type":              "text",
		"text":              map[string]string{"body": message},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://graph.facebook.com/v18.0/%s/messages", phoneNumberID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[whatsapp] error status=%d body=%s", resp.StatusCode, string(respBody))
		return fmt.Errorf("whatsapp API returned %d", resp.StatusCode)
	}
	return nil
}

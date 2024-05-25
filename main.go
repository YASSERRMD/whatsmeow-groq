package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal"
	_ "github.com/joho/godotenv/autoload"

	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Define a struct to match the response JSON structure
type ApiResponse struct {
	Response string `json:"response"`
}

// Define a struct for the request body
type RequestBody struct {
	UserID  string `json:"user_id"`
	Message string `json:"message"`
}

// Define a struct for the request payload
type RequestPayload struct {
	Messages []Message `json:"messages"`
	Model    string    `json:"model"`
}

// Define a struct for the messages array
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Define a struct to match the response JSON structure
type ApiResponseGroq struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Function to send POST request to the API
func sendPostRequestGroq(prompt string) (string, error) {
	// Set up the request payload
	payload := RequestPayload{
		Messages: []Message{
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Model: "mixtral-8x7b-32768",
	}

	// Marshal the request payload into JSON
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("error marshaling JSON: %w", err)
	}

	// Create a new HTTP request
	req, err := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("error creating request: %w", err)
	}

	// Get the API key from environment variables
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("GROQ_API_KEY environment variable not set")
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Initialize HTTP client
	client := &http.Client{}

	// Send the request
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	// Read the response body
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %w", err)
	}

	// Unmarshal the JSON response into ApiResponse struct
	var apiResp ApiResponseGroq
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		return "", fmt.Errorf("error unmarshaling response JSON: %w", err)
	}

	// Return the content from the first choice's message
	if len(apiResp.Choices) > 0 {
		return apiResp.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("no choices found in the response")
}

func htmlToWhatsAppFormat(html string) string {
	// Replace HTML tags with WhatsApp-friendly formatting
	html = strings.ReplaceAll(html, "</p>\n", "\n")
	html = strings.ReplaceAll(html, "</p>", "\n")
	html = strings.ReplaceAll(html, "<p>", "")
	html = strings.ReplaceAll(html, "<ol>", "- ")
	html = strings.ReplaceAll(html, "</ol>", "\n")
	html = strings.ReplaceAll(html, "<li>", "- ")
	html = strings.ReplaceAll(html, "</li>", "\n")
	html = strings.ReplaceAll(html, "<br>", "\n")

	return html
}

func GetEventHandler(client *whatsmeow.Client) func(interface{}) {
	return func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			var messageBody = v.Message.GetConversation()
			if messageBody == "0>" || strings.Contains(messageBody, "0>") {
				var chatMsg = strings.ReplaceAll(messageBody, "0> ", "")
				userDetail := v.Info.Sender.User

				fmt.Println("The user name is:", userDetail)
				message := chatMsg

				respMessage, err := sendPostRequestGroq(message)
				if err != nil {
					fmt.Println("Failed to send post request:", err)
				}

				client.SendMessage(context.Background(), v.Info.Chat, &waProto.Message{
					Conversation: proto.String(respMessage),
				})
			}
		}
	}
}

func main() {

	dbLog := waLog.Stdout("Database", "DEBUG", true)
	// Make sure you add appropriate DB connector imports, e.g. github.com/mattn/go-sqlite3 for SQLite as we did in this minimal working example
	container, err := sqlstore.New("sqlite3", "file:examplestore.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}

	// If you want multiple sessions, remember their JIDs and use .GetDevice(jid) or .GetAllDevices() instead.
	deviceStore, err := container.GetFirstDevice()
	if err != nil {
		panic(err)
	}

	clientLog := waLog.Stdout("Client", "INFO", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(GetEventHandler(client))

	if client.Store.ID == nil {
		// No ID stored, new login
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			panic(err)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				// Render the QR code here
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				// or just manually `echo 2@... | qrencode -t ansiutf8` in a terminal:
				// fmt.Println("QR code:", evt.Code)
			} else {
				fmt.Println("Login event:", evt.Event)
			}
		}
	} else {
		// Already logged in, just connect
		err = client.Connect()
		if err != nil {
			panic(err)
		}
	}

	// Listen to Ctrl+C (you can also do something else that prevents the program from exiting)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	client.Disconnect()
}

package queue

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/pubsub"
)

// Variables used for Slack validation that will not change.
const (
	version                     = "v0"
	slackRequestTimestampHeader = "X-Slack-Request-Timestamp"
	slackSignatureHeader        = "X-Slack-Signature"
)

// Variables used for the GCP Pub/Sub connection.
var (
	projectID string
	topicName string
)

// Variables used for Slack validation.
var (
	slackSigSecret string
	slackChannelID string
)

// Struct for the message to be sent to the GCP Pub/Sub engine.
type queueMessage struct {
	Query       string `json:"query"`
	ResponseUrl string `json:"response_url"`
}

// Struct for the message to be sent back to Slack after the
// initial contact.
type queueResponse struct {
	ResponseType string `json:"response_type"`
	Text         string `json:"text"`
}

// init() runs at the beginning of our GCF and sets the variables needed
// for the queue process from the env variables set in the GCF.
func init() {
	projectID = os.Getenv("GCP_PROJECT_ID")
	topicName = os.Getenv("GCP_TOPIC_NAME")

	slackSigSecret = os.Getenv("SLACK_SIG_SECRET")
	slackChannelID = os.Getenv("SLACK_CHANNEL_ID")
}

// main() does not run in GCF. It is left here strictly for testing
// responses locally. To compile locally, change package name
// to "main" and run `go build`.
func main() {
	http.HandleFunc("/queue", Queue)

	err := http.ListenAndServe(":1234", nil)
	if err != nil {
		log.Fatalf("Could not serve http: %v", err)
	}
}

// Main entry point for GCF anerbot-queue function. An HTTP request
// to the cloud function is sent directly to Queue() and the rest
// of the process launches from this point.
func Queue(w http.ResponseWriter, r *http.Request) {
	// Immediately reply if query string "ping" is not empty.
	// This can be used by an external caller to keep the
	// GCF warm for responses.
	if r.URL.Query().Get("ping") != "" {
		return
	}

	// Grab the raw body in bytes from the original request and
	// create a readable buffer for other functions to use.
	bodyBytes, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Fatalf("Couldn't read request body: %v", err)
	}
	r.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))

	// Check if the method of the request was a "POST". Messages
	// from Slack should not come in any other method.
	if r.Method != "POST" {
		http.Error(w, "Only POST requests are accepted", 405)
		return
	}

	// Parse the body of the POST request and gather the data
	// into a new field on the request called Form (accessed
	// via r.Form)
	if err := r.ParseForm(); err != nil {
		log.Fatalf("ParseForm: %v", err)
	}

	// Reset r.Body field as ParseForm depletes it by reading
	// the io.ReadCloser.
	r.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))

	// Validate that our request is legitimate and actually came
	// from Snyk's Slack.
	ok, err := verifyWebHook(r, slackSigSecret)
	if err != nil {
		log.Fatalf("verifyWebhook: %v", err)
	}
	if !ok {
		log.Fatalf("unable to validate request: signatures did not match")
	}

	// Validate that the entire form is actually present.
	if len(r.Form["text"]) == 0 {
		log.Fatalf("empty text in form")
	}

	// End technical request validation.
	// Begin user request validation.

	// Create our object that will eventually be converted to
	// JSON and sent to Slack as a response.
	res := queueResponse{
		ResponseType: "ephemeral",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Validate that the request came from the restricted Slack channel ID.
	if r.Form["channel_id"][0] != slackChannelID {
		res.Text = fmt.Sprintf("Anerbot needs to run in <#%s>, try again there! :broken_heart:", slackChannelID)
		// Marshal our response struct into JSON and send it back to Slack.
		err = json.NewEncoder(w).Encode(res)
		if err != nil {
			log.Fatalf("json.Marshal: %v", err)
		}
		return
	}

	// Validate the query itself from the form. Check for
	// an empty query and omit the word "search" if present
	// to maintain backwards compatibility with Anerbot 1.0.
	queryText := r.Form["text"][0]
	if queryText == "" {
		res.Text = fmt.Sprintf("Unable search for an empty string! :this-is-fine:")
		// Marshal our response struct into JSON and send it back to Slack.
		err = json.NewEncoder(w).Encode(res)
		if err != nil {
			log.Fatalf("json.Marshal: %v", err)
		}
		return
	}
	if strings.HasPrefix(queryText, "search") {
		queryText = strings.TrimPrefix(queryText, "search ")
	}

	// Prepare the message to the queue made up of two
	// components: the query from the user, and the URL that
	// Slack will be listening on for additional messages.
	message := queueMessage{
		Query:       queryText,
		ResponseUrl: r.Form["response_url"][0],
	}

	// Send the message (publish) to the GCP Pub/Sub engine.
	// As soon as a message is received, the GCF anerbot-response
	// function is kicked off and operates on the message.
	err = publishMessage(message)
	if err != nil {
		log.Fatalf("unable to publish message: %v", err)
	}

	// Prepare the message to be immediately sent back to Slack
	// in an attempt to beat their three second timeout.
	res.Text = fmt.Sprintf(`Hang tight - gathering results for "%s".`, queryText)

	// Marshal our response struct into JSON and send it back to Slack.
	err = json.NewEncoder(w).Encode(res)
	if err != nil {
		log.Fatalf("json.Marshal: %v", err)
	}
}

// Function to send our message to the GCP Pub/Sub Engine.
func publishMessage(message queueMessage) error {
	// Marshal our message struct into JSON.
	m, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("unable to convert message to json: %v", err)
	}

	// Create a new Pub/Sub client that will allow further operations.
	// The client automatically pulls authentication credentials
	// from the Service Account running to anerbot-queue Cloud
	// Function, anerbot. If this function is being run locally for
	// testing purposes, the `GOOGLE_APPLICATION_CREDENTIALS` env
	// variable must be set and pointing to a GCP JSON credential
	// file for the anerbot Service Account.
	ctx := context.Background()
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		return fmt.Errorf("unable to create pubsub client: %v", err)
	}

	// Set the Topic to be used, usually "anerbot" but configurable
	// in the GCF environment variables, and publish the message.
	t := client.Topic(topicName)
	result := t.Publish(ctx, &pubsub.Message{
		Data: m,
	})

	// Ensure the publishing was successful. Throw away the result.
	_, err = result.Get(ctx)
	if err != nil {
		return fmt.Errorf("unable to get published result: %v", err)
	}

	return nil
}

// Function to validate that the request we received was actually from Slack.
func verifyWebHook(r *http.Request, slackSigningSecret string) (bool, error) {
	// Set basic control data  from the request itself.
	timeStamp := r.Header.Get(slackRequestTimestampHeader)
	slackSignature := r.Header.Get(slackSignatureHeader)

	// Convert the timestamp into an integer for comparing.
	t, err := strconv.ParseInt(timeStamp, 10, 64)
	if err != nil {
		return false, fmt.Errorf("strconv.ParseInt(%s): %v", timeStamp, err)
	}

	// Validate that the time this message was sent was within the last five minutes.
	if ageOk, age := checkTimestamp(t); !ageOk {
		return false, fmt.Errorf("checkTimestamp(%v): %v %v", t, ageOk, age)
	}

	// Verify that the headers actually contained the needed controls.
	if timeStamp == "" || slackSignature == "" {
		return false, fmt.Errorf("either timeStamp or signature headers were blank")
	}

	// Generate a slice of bytes representing the body for hashing.
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return false, fmt.Errorf("ioutil.ReadAll(%v): %v", r.Body, err)
	}

	// Reset the body so other calls won't fail.
	r.Body = ioutil.NopCloser(bytes.NewBuffer(body))

	// Create the string used to validate the signature. The string is
	// based on the Slack version (which is always "v0"), the timestamp,
	// and the body itself.
	baseString := fmt.Sprintf("%s:%s:%s", version, timeStamp, body)

	// Generate the signature of this request based on all the parts and the
	// original signing secret from Slack.
	signature := getSignature([]byte(baseString), []byte(slackSigningSecret))

	// Drop the "v0=" off the front of the signature since the computed
	// one will not have it. Convert the trimmed hex string into bytes.
	trimmed := strings.TrimPrefix(slackSignature, fmt.Sprintf("%s=", version))
	signatureInHeader, err := hex.DecodeString(trimmed)
	if err != nil {
		return false, fmt.Errorf("hex.DecodeString(%v): %v", trimmed, err)
	}

	// Compare the two values and return true if they are a match.
	return hmac.Equal(signature, signatureInHeader), nil
}

// Function to validate the time of the request being set.
func checkTimestamp(timeStamp int64) (bool, time.Duration) {
	t := time.Since(time.Unix(timeStamp, 0))

	// Arbitrarily trusting messages sent within the last five minutes.
	return t.Minutes() <= 5, t
}

// Function to generate a checksum used to compare the secrets.
func getSignature(base []byte, secret []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(base)

	return h.Sum(nil)
}

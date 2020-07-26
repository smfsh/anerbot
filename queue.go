package anerbot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/pubsub"

	"github.com/labstack/gommon/log"
)

const (
	version                     = "v0"
	slackRequestTimestampHeader = "X-Slack-Request-Timestamp"
	slackSignatureHeader        = "X-Slack-Signature"
)

var (
	projectID string
	topicName string
)

var (
	slackSigSecret string
	slackChannelID string
)

type queueMessage struct {
	Query       string `json:"query"`
	ResponseUrl string `json:"response_url"`
}

type queueResponse struct {
	ResponseType string `json:"response_type"`
	Text         string `json:"text"`
}

func init() {
	projectID = os.Getenv("GCP_PROJECT_ID")
	topicName = os.Getenv("GCP_TOPIC_NAME")

	slackSigSecret = os.Getenv("SLACK_SIG_SECRET")
	slackChannelID = os.Getenv("SLACK_CHANNEL_ID")
}

func main() {
	http.HandleFunc("/response", LocalResponse)
	http.HandleFunc("/queue", Queue)

	err := http.ListenAndServe(":1234", nil)
	if err != nil {
		log.Fatalf("Could not serve http: %v", err)
	}
}

func Queue(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Fatalf("Couldn't read request body: %v", err)
	}
	r.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))

	if r.Method != "POST" {
		http.Error(w, "Only POST requests are accepted", 405)
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Couldn't parse form", 400)
		log.Fatalf("ParseForm: %v", err)
	}

	// Reset r.Body as ParseForm depletes it by reading the io.ReadCloser.
	r.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))
	ok, err := verifyWebHook(r, slackSigSecret)
	if err != nil {
		log.Fatalf("verifyWebhook: %v", err)
	}
	if !ok {
		log.Fatalf("signatures did not match.")
	}

	if len(r.Form["text"]) == 0 {
		log.Fatalf("empty text in form")
	}

	queryText := r.Form["text"][0]
	if queryText == "" {
		http.Error(w, "Unable to search for an empty string", 400)
	}
	if strings.HasPrefix(queryText, "search") {
		queryText = strings.TrimPrefix(queryText, "search ")
	}

	message := queueMessage{
		Query:       queryText,
		ResponseUrl: r.Form["response_url"][0],
	}

	err = publishMessage(message)
	if err != nil {
		log.Fatalf("unable to publish message: %v", err)
	}

	responseText := fmt.Sprintf(`Hang tight - gathering results for "%s".`, queryText)

	res := queueResponse{
		ResponseType: "ephemeral",
		Text:         responseText,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	err = json.NewEncoder(w).Encode(res)
	if err != nil {
		log.Fatalf("json.Marshal: %v", err)
	}
}

func publishMessage(message queueMessage) error {
	m, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("unable to convert message to json: %v", err)
	}

	ctx := context.Background()
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		return fmt.Errorf("unable to create pubsub client: %v", err)
	}

	t := client.Topic(topicName)
	result := t.Publish(ctx, &pubsub.Message{
		Data: m,
	})

	_, err = result.Get(ctx)
	if err != nil {
		return fmt.Errorf("unable to get published result: %v", err)
	}

	return nil
}

func verifyWebHook(r *http.Request, slackSigningSecret string) (bool, error) {
	timeStamp := r.Header.Get(slackRequestTimestampHeader)
	slackSignature := r.Header.Get(slackSignatureHeader)

	t, err := strconv.ParseInt(timeStamp, 10, 64)
	if err != nil {
		return false, fmt.Errorf("strconv.ParseInt(%s): %v", timeStamp, err)
	}

	if ageOk, age := checkTimestamp(t); !ageOk {
		return false, fmt.Errorf("checkTimestamp(%v): %v %v", t, ageOk, age)
	}

	if timeStamp == "" || slackSignature == "" {
		return false, fmt.Errorf("either timeStamp or signature headers were blank")
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return false, fmt.Errorf("ioutil.ReadAll(%v): %v", r.Body, err)
	}

	// Reset the body so other calls won't fail.
	r.Body = ioutil.NopCloser(bytes.NewBuffer(body))

	baseString := fmt.Sprintf("%s:%s:%s", version, timeStamp, body)

	signature := getSignature([]byte(baseString), []byte(slackSigningSecret))

	trimmed := strings.TrimPrefix(slackSignature, fmt.Sprintf("%s=", version))
	signatureInHeader, err := hex.DecodeString(trimmed)

	if err != nil {
		return false, fmt.Errorf("hex.DecodeString(%v): %v", trimmed, err)
	}

	return hmac.Equal(signature, signatureInHeader), nil
}

// Arbitrarily trusting requests time stamped less than 5 minutes ago.
func checkTimestamp(timeStamp int64) (bool, time.Duration) {
	t := time.Since(time.Unix(timeStamp, 0))

	return t.Minutes() <= 5, t
}

func getSignature(base []byte, secret []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(base)

	return h.Sum(nil)
}

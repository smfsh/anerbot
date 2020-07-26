package response

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/smfsh/airtable-go"
)

// Variables used for the Airtable connection.
var (
	airtableAPIKey  string
	airtableBaseID  string
	airtableTableID string
	airtableViewID  string
)

// Struct to contain each "feature" returned from an Airtable query.
type feature struct {
	AirtableID string `json:"id"`
	Fields     struct {
		Feature         string
		Roadmap         string
		TeamResponsible string `json:"Team responsible"`
		Plan            string
		FeatureFlag     string `json:"Feature flag"`
		Entitlements    string
		Documentation   string
	}
}

// Struct for the message to be sent to Slack.
type slackResponse struct {
	ReplaceOriginal string       `json:"replace_original"`
	ResponseType    string       `json:"response_type"`
	Text            string       `json:"text"`
	Attachments     []attachment `json:"attachments,omitempty"`
}

// Struct for each attachment in the Slack message. Each of
// these represents one unique "feature". Title is what will
// normally be displayed to a user and fallback will be used
// in the event that rich markdown cannot be rendered.
type attachment struct {
	Title     string            `json:"title"`
	Fallback  string            `json:"fallback"`
	TitleLink string            `json:"title_link"`
	Fields    []attachmentField `json:"fields"`
}

// Struct to represent the information printed to the requester
// in Slack for each "feature". The title field should always
// be blank and value will always contain markdown.
type attachmentField struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// Struct for the message to be received from the GCP Pub/Sub engine.
type PubSubMessage struct {
	Data []byte `json:"data"`
}

// Struct for the message to be received from the GCP Pub/Sub engine.
type queueMessage struct {
	Query       string `json:"query"`
	ResponseUrl string `json:"response_url"`
}

// init() runs at the beginning of our GCF and sets the variables needed
// for the response process from the env variables set in the GCF.
func init() {
	airtableAPIKey = os.Getenv("AIRTABLE_API_KEY")
	airtableBaseID = os.Getenv("AIRTABLE_BASE_ID")
	airtableTableID = os.Getenv("AIRTABLE_TABLE_ID")
	airtableViewID = os.Getenv("AIRTABLE_VIEW_ID")
}

// main() does not run in GCF. It is left here strictly for testing
// responses locally. To compile locally, change package name
// to "main" and run `go build`.
func main() {
	http.HandleFunc("/response", LocalResponse)

	err := http.ListenAndServe(":1234", nil)
	if err != nil {
		log.Fatalf("Could not serve http: %v", err)
	}
}

// Main entry point for GCF anerbot-response function. When a new message
// is added to the anerbot Topic in Pub/Sub, this function is called and
// the message is passed into it as an argument.
func Response(ctx context.Context, m PubSubMessage) error {
	// Unmarshal the JSON contained in the message. The message
	// will contain the original search term and the Slack URL where
	// the final results can be posted to.
	var message queueMessage
	err := json.Unmarshal(m.Data, &message)
	if err != nil {
		return fmt.Errorf("could not unmarshal message: %v", err)
	}

	// Perform the search in Airtable, passing in the original query term.
	// Respond with a failure message if Airtable is unreachable for any reason.
	atr, err := queryAirtable(message.Query)
	if err != nil {
		sendFailureMessage(message.ResponseUrl)
		return fmt.Errorf("error querying Airtable: %v", err)
	}

	// Build the full response object to be sent back to Slack.
	res, err := buildSlackResponse(atr)
	if err != nil {
		return fmt.Errorf("unable to build slack response: %v", err)
	}

	// Marshal the response object into JSON and prepare the request to be
	// sent to the ResponseUrl that was in the original message.
	body, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("unable to convert slack message to JSON: %v", err)
	}
	req, err := http.NewRequest("POST", message.ResponseUrl, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("unable to build new HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Perform the request (posting our message to Slack,) and
	// close out the response body sent back.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("unable to send message to Slack: %v", err)
	}
	defer resp.Body.Close()
	return nil
}

// Function to send a message to Slack informing the user that the program
// was unable to communicate with Slack.
func sendFailureMessage(url string) {
	// Prepare message to be sent to Slack.
	message := slackResponse{
		ResponseType: "ephemeral",
		Text:         "Failed to fetch records from Airtable :sob:",
	}

	// Marshal the message into JSON and prepare the request to be sent
	// to the URL passed into this function. This should always be the
	// ResponseUrl field from the original message.
	body, err := json.Marshal(message)
	if err != nil {
		log.Fatalf("unable to convert slack message to JSON: %v", err)
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		log.Fatalf("unable to build new HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Perform the request (posting our message to Slack,) and
	// close out the response body sent back.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("unable to send message to Slack: %v", err)
	}
	defer resp.Body.Close()
}

// Function utilized strictly for local testing of the response object
// to be sent back to Slack. In order to use this function, change this
// package name to "main" and run `go build`.
func LocalResponse(w http.ResponseWriter, r *http.Request) {
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
	}

	// Parse the body of the POST request and gather the data
	// into a new field on the request called Form (accessed
	// via r.Form)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Couldn't parse form", 400)
		log.Fatalf("ParseForm: %v", err)
	}

	// Reset r.Body field as ParseForm depletes it by reading
	// the io.ReadCloser.
	r.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))

	// Validate the query itself from the form. Check for
	// an empty query and omit the word "search" if present
	// to maintain backwards compatibility with Anerbot 1.0.
	queryText := r.Form["text"][0]
	if strings.HasPrefix(queryText, "search") {
		queryText = strings.TrimPrefix(queryText, "search ")
	}

	// Perform the search in Airtable, passing in the original query term.
	// Respond with a failure message if Airtable is unreachable for any reason.
	atr, err := queryAirtable(queryText)
	if err != nil {
		log.Fatalf("error querying Airtable: %v", err)
	}

	// Build the full response object to be sent back to Slack.
	res, err := buildSlackResponse(atr)
	if err != nil {
		log.Fatalf("unable to build slack response: %v", err)
	}

	// Marshal our response struct into JSON and respond to the request.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	err = json.NewEncoder(w).Encode(res)
	if err != nil {
		log.Fatalf("json.Marshal: %v", err)
	}
}

// Function to build the response to be sent to Slack. The slackResponse
// object will contain all the data needed for Slack to display the message.
func buildSlackResponse(f []feature) (*slackResponse, error) {
	// Prepare the top level statement of our results which reports
	// whether there were any results from Airtable or not by counting
	// the slice of features (f) passed into the function.
	var text string
	if len(f) == 0 {
		text = "No items found, try another search term"
	} else {
		text = fmt.Sprintf("Found %d items! Click on any result to learn more.", len(f))
	}

	// Initialize the response object with some default values.
	res := &slackResponse{
		ReplaceOriginal: strconv.FormatBool(true),
		ResponseType:    "ephemeral",
		Text:            text,
		Attachments:     nil,
	}

	// Prepare an attachment object for each feature in the feature slice.
	for _, v := range f {
		// Generate a link to this specific feature in Airtable.
		link := fmt.Sprintf("https://airtable.com/%s/%s/%s", airtableTableID, airtableViewID, v.AirtableID)

		// Create a single string that represents each possible field from
		// Airtable. Each part is concatenated to the previous part. Fields
		// are visually separated in Slack via the inclusion of `\r\n` which
		// represents a return and new line.
		var value string
		if v.Fields.Roadmap != "" {
			value += fmt.Sprintf(":sparkles: *Roadmap:* %s\r\n", v.Fields.Roadmap)
		}
		if v.Fields.TeamResponsible != "" {
			value += fmt.Sprintf(":one-team: *Team(s):* %s\r\n", v.Fields.TeamResponsible)
		}
		if v.Fields.Plan != "" {
			value += fmt.Sprintf(":moneybag: *Plan:* %s\r\n", v.Fields.Plan)
		}
		if v.Fields.FeatureFlag != "" {
			value += fmt.Sprintf(":triangular_flag_on_post: *Feature Flag:* %s\r\n", v.Fields.FeatureFlag)
		}
		if v.Fields.Entitlements != "" {
			value += fmt.Sprintf(":crown: *Entitlements:* %s\r\n", v.Fields.Entitlements)
		}
		if v.Fields.Documentation != "" {
			value += fmt.Sprintf(":books: *Documentation:* %s\r\n", v.Fields.Documentation)
		}

		// Create a fallback title to be used in the case that rich markdown
		// isn't available in the Slack client. This will come out in the
		// following format: "Name of Feature: https://url.to/feature/in/airtable"
		fallback := fmt.Sprintf("%s: %s", v.Fields.Feature, link)

		// Add all of our crafted items to fields of an attachment object.
		// Add the attachment object to the attachments field of the response.
		res.Attachments = append(res.Attachments, attachment{
			Title:     v.Fields.Feature,
			Fallback:  fallback,
			TitleLink: link,
			Fields: []attachmentField{
				{
					Title: "",
					Value: value,
				},
			},
		})
	}

	// Return the Slack response object.
	return res, nil
}

// Function to query Airtable for a search term.
func queryAirtable(query string) ([]feature, error) {
	// Initiate an Airtable client that will allow further operations.
	client, err := airtable.New(airtableAPIKey, airtableBaseID)
	if err != nil {
		return nil, fmt.Errorf("unable to create new airtable client: %v", err)
	}

	// Convert our query to lowercase to gather the most results.
	query = strings.ToLower(query)

	// Create a slice of strings containing each of the fields
	// that should be queried in Airtable.
	var fields = []string{
		"Feature",
		"Roadmap",
		"Team responsible",
		"Plan",
		"Feature flag",
		"Entitlements",
		"Documentation",
	}

	// Create an empty slice of strings that will be filled with
	// strings representing an Airtable-compatible query-statement.
	// There will be one statement created for each of the fields
	// in the fields slice.
	var searchStatements []string
	for _, v := range fields {
		statement := fmt.Sprintf("SEARCH('%s', LOWER({%s})) > 0", query, v)
		searchStatements = append(searchStatements, statement)
	}

	// Create a single string, formula, by combining each of the elements
	// in the searchStatements slice, separated by a comma.
	var formula = fmt.Sprintf("OR(%s)", strings.Join(searchStatements, ", "))

	// Initialize and populate the listParams object that will be
	// used by the Airtable client to create a result set.
	listParams := airtable.ListParameters{
		CellFormat:      "string",
		Fields:          fields,
		FilterByFormula: formula,
		TimeZone:        "American/Boston",
		UserLocale:      "en-US",
		View:            airtableViewID,
	}

	// Initialize an empty slice of features to contain our results.
	var features []feature

	// Populate the features variable with results from Airtable.
	err = client.ListRecords(airtableTableID, &features, listParams)
	if err != nil {
		return nil, err
	}

	// Return the slice of features for further processing.
	return features, nil
}

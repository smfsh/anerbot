package anerbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	"github.com/smfsh/airtable-go"

	"github.com/labstack/gommon/log"
)

var (
	airtableAPIKey  string
	airtableBaseID  string
	airtableTableID string
	airtableViewID  string
)

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

type slackResponse struct {
	ReplaceOriginal bool         `json:"replace_original"`
	ResponseType    string       `json:"response_type"`
	Text            string       `json:"text"`
	Attachments     []attachment `json:"attachments,omitempty"`
}

type attachment struct {
	Title     string            `json:"title"`
	Fallback  string            `json:"fallback"`
	TitleLink string            `json:"title_link"`
	Fields    []attachmentField `json:"fields"`
}

type attachmentField struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

type PubSubMessage struct {
	Data []byte `json:"data"`
}

func init() {
	airtableAPIKey = os.Getenv("AIRTABLE_API_KEY")
	airtableBaseID = os.Getenv("AIRTABLE_BASE_ID")
	airtableTableID = os.Getenv("AIRTABLE_TABLE_ID")
	airtableViewID = os.Getenv("AIRTABLE_VIEW_ID")
}

func Response(ctx context.Context, m PubSubMessage) error {
	var message queueMessage
	err := json.Unmarshal(m.Data, &message)
	if err != nil {
		return fmt.Errorf("could not unmarshal message: %v", err)
	}

	atr, err := queryAirtable(message.Query)
	if err != nil {
		sendFailureMessage(message.ResponseUrl)
		return fmt.Errorf("error querying Airtable: %v", err)
	}

	res, err := buildSlackResponse(atr)
	if err != nil {
		return fmt.Errorf("unable to build slack response: %v", err)
	}

	body, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("unable to convert slack message to JSON: %v", err)
	}
	req, err := http.NewRequest("POST", message.ResponseUrl, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("unable to build new HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("unable to send message to Slack: %v", err)
	}
	defer resp.Body.Close()
	return nil
}

func sendFailureMessage(url string) {
	message := slackResponse{
		ResponseType: "ephemeral",
		Text:         "Failed to fetch records from Airtable :sob:",
	}
	body, err := json.Marshal(message)
	if err != nil {
		log.Fatalf("unable to convert slack message to JSON: %v", err)
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		log.Fatalf("unable to build new HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("unable to send message to Slack: %v", err)
	}
	defer resp.Body.Close()
}

func LocalResponse(w http.ResponseWriter, r *http.Request) {
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

	queryText := r.Form["text"][0]
	if strings.HasPrefix(queryText, "search") {
		queryText = strings.TrimPrefix(queryText, "search ")
	}

	atr, err := queryAirtable(queryText)
	if err != nil {
		log.Fatalf("error querying Airtable: %v", err)
	}

	res, err := buildSlackResponse(atr)
	if err != nil {
		log.Fatalf("unable to build slack response: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	err = json.NewEncoder(w).Encode(res)
	if err != nil {
		log.Fatalf("json.Marshal: %v", err)
	}
}

func buildSlackResponse(f []feature) (*slackResponse, error) {
	var text string
	if len(f) == 0 {
		text = "No items found, try another search term"
	} else {
		text = fmt.Sprintf("Found %d items! Click on any result to learn more.", len(f))
	}
	res := &slackResponse{
		ReplaceOriginal: true,
		ResponseType:    "ephemeral",
		Text:            text,
		Attachments:     nil,
	}
	for _, v := range f {
		link := fmt.Sprintf("https://airtable.com/%s/%s/%s", airtableTableID, airtableViewID, v.AirtableID)

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

		fallback := fmt.Sprintf("%s: %s", v.Fields.Feature, link)

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

	return res, nil
}

func queryAirtable(query string) ([]feature, error) {
	client, err := airtable.New(airtableAPIKey, airtableBaseID)
	if err != nil {
		return nil, fmt.Errorf("unable to create new airtable client: %v", err)
	}

	query = strings.ToLower(query)

	var fields = []string{
		"Feature",
		"Roadmap",
		"Team responsible",
		"Plan",
		"Feature flag",
		"Entitlements",
		"Documentation",
	}

	var searchStatements []string

	for _, v := range fields {
		statement := fmt.Sprintf("SEARCH('%s', LOWER({%s})) > 0", query, v)
		searchStatements = append(searchStatements, statement)
	}

	var formula = fmt.Sprintf("OR(%s)", strings.Join(searchStatements, ", "))

	listParams := airtable.ListParameters{
		CellFormat:      "string",
		Fields:          fields,
		FilterByFormula: formula,
		TimeZone:        "American/Boston",
		UserLocale:      "en-US",
		View:            airtableViewID,
	}

	var features []feature

	err = client.ListRecords(airtableTableID, &features, listParams)
	if err != nil {
		return nil, err
	}

	return features, nil
}

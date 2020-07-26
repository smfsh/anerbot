## Anerbot

Get a little Aner with every bite.

Anerbot is a Slack bot designed to query Airtable for a search term specified by a user in Slack. It relies
on Google Cloud's Cloud Functions to run two distinct operations: queue and response.

When a search is initiated in Slack (such as `/feat search golang`), Slack sends a POST request to a Google
Cloud Function `anerbot-queue` via an HTTP listener. The function performs two distinct jobs upon invocation:

* Add a message to the Google Cloud Pub/Sub messaging service containing details about the query
* Immediately respond back to Slack to alert the user that the request has been received

The Google Cloud Pub/Sub service, in response to receiving the message, immediately triggers the second function
to run: `anerbot-response`. This function takes in the message from Pub/Sub, queries Airtable, builds a stylized
Slack message in JSON format, then sends the message back to Slack.

#### Setup
 
The service expects environment variables to be setup when both functions are created. No secrets are hardcoded
into the functions themselves. The environment variables needed are listed here:
 
* `GCP_PROJECT_ID`: environment name or ID used to identify the Google Cloud instance containing the functions
* `GCP_TOPIC_NAME`: name of the topic setup in Google Cloud Pub/Sub
* `SLACK_SIG_SECRET`: validation signature from the Slack application to validate message signing
* `SLACK_CHANNEL_ID`: channel ID from Slack used to validate request origin authenticity
* `AIRTABLE_API_KEY`: API key for the Airtable account performing the query action
* `AIRTABLE_BASE_ID`: base ID for the Airtable instance queried
* `AIRTABLE_TABLE_ID`: table ID for the Airtable table queried
* `AIRTABLE_VIEW_ID`: view ID for the Airtable view queried

In order for both functions to work, the Google Cloud Pub/Sub service must have a topic configured. A new topic
can be created in the Google Cloud interface or with `gcloud pubsub topics create anerbot` if you have the GCP
CLI tooling installed and configured.

To setup the `anerbot-queue` function, set the function to run as a Service Account that has permissions to
write messages to the GCP Pub/Sub service. The credentials for this user will be automatically passed through
to the Pub/Sub client inside the function. Additionally, configure the `Trigger type` to be `HTTP`. The URL from
this trigger function should be placed into the slash command configuration in the Slack app. The entry point
for this function is `Queue()`.

To setup the `anerbot-response` function, set the function to run as the same service account used for `anerbot-queue`.
This is, strictly speaking, unnecessary today, but could be used for additional messaging features in future
development. The `Trigger type` should be set to `Cloud Pub/Sub` and the topic created earlier should be selected.
The entry point for this function is `Response()`.

#### Testing

For local testing, both services contain a local web server that can take a request to simulate the action to
occur on GCF. In order to use either of these services locally, edit the files and change the package name to
`main`. Then compile the application with `go run`. Remember that the environment variables have to all be set
locally for local testing to work. Additionally, a service account in GCP that has access to publish messages
to the Pub/Sub service must have a credential JSON file created and pointed to with the `GOOGLE_APPLICATION_CREDENTIALS`
environment variable. See inline comments for more details.

For `anerbot-queue`, a simulated message from Slack looks like this:

```http request
token=gIkuvaNzQIHg97ATvDxqgjtO
&team_id=T0001
&team_domain=example
&enterprise_id=E0001
&enterprise_name=Globular%20Construct%20Inc
&channel_id=C2147483705
&channel_name=test
&user_id=U2147483697
&user_name=Steve
&command=/feat
&text=search golang
&response_url=https://hooks.slack.com/commands/1234/5678
&trigger_id=13345224609.738474920.8088930838d88f008e0
```

For `anerbot-response`, the same message can be sent to get an example of the JSON object that would be
sent to Slack in a reply, including the Airtable results. To more properly test the method that Pub/Sub
would trigger the function, add a call to `Response()` in the `main()` and pass the following "message"
to the function:

```json5
{
    "data": "eyJxdWVyeSI6ImdvbGFuZyIsInJlc3BvbnNlX3VybCI6Imh0dHBzOi8vaG9va3Muc2xhY2suY29tL2NvbW1hbmRzLzEyMzQvNTY3OCJ9",
    "messageId": "string",
    "publishTime": "string"
}
```

For more immediate testing, the same JSON object can be placed into the "Testing" tab for the function in the
Google Cloud Console. Note: the testing in the console will only return status of the container execution and
not necessarily any valuable output, but this can still be used to validate general integrity.

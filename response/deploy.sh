#!/bin/bash

go mod tidy
go mod vendor
gcloud functions deploy anerbot-response

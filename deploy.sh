#!/bin/bash

echo "***** Beginning deployments *****"

./queue/deploy.sh &
./response/deploy.sh &

wait
echo "***** Finished deploymemts *****"

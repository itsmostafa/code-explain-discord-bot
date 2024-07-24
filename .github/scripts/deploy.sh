#!/bin/bash

echo "Stopping existing container.."
docker stop code-explain

echo "Pulling latest image.."
docker pull ghcr.io/itsmostafa/code-explain:latest

echo "Launching latest container"
docker run -d --rm --env-file .env --name code-explain ghcr.io/itsmostafa/code-explain:latest

echo "Deployed"
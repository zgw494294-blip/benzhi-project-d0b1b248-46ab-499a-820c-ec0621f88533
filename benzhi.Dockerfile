FROM --platform=$TARGETPLATFORM golang:1.27.0
WORKDIR /app
COPY go.mod ./
RUN GOTOOLCHAIN=local go mod download
COPY . .
RUN GOTOOLCHAIN=local go build ./...
CMD ["bash"]

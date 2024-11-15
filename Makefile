all:

install:
	brew install opencv

run-server:
	cd server && go run main.go
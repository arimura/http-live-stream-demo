all:

install:
	brew install opencv

run-server: clean-hls
	cd server && go run main.go

clean-hls:
	rm -rf server/hls/*
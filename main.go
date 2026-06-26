package main

import (
	"chat_image/api"
	"chat_image/service"
)

func main() {
	service.Run(api.Register)
}

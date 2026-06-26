package main

import (
	"chat_image/app/api"
	"chat_image/app/service"
)

func main() {
	service.Run(api.Register)
}

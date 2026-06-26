package service

import "chat_image/app/storage"

type ObjectStorage = storage.ObjectStorage

var NewObjectStorageFromConfig = storage.NewObjectStorageFromConfig
var gatewayObjectKey = storage.ObjectKey

package service

import "chat_image/storage"

type ObjectStorage = storage.ObjectStorage

var NewObjectStorageFromConfig = storage.NewObjectStorageFromConfig
var gatewayObjectKey = storage.ObjectKey

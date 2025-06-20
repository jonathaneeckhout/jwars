class_name Client
extends Node

var client: WebSocketClient = null

func _ready() -> void:
    client = WebSocketClient.new()
    client.name = "WebsocketClient"
    add_child(client)
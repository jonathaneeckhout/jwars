class_name Server
extends Node

var server: WebSocketServer = null

func _ready() -> void:
    server = WebSocketServer.new()
    server.name = "WebsocketServer"
    add_child(server)

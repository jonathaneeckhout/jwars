extends Node2D

var server: Server = null
var client: Client = null

func _ready() -> void:
    if "--server" in OS.get_cmdline_args():
        start_server()
    elif "--client" in OS.get_cmdline_args():
        start_client()


func start_server():
    get_window().title = "JWars (server)"

    server = Server.new()
    server.name = "Server"
    add_child(server)

func start_client():
    get_window().title = "JWars (client)"

    client = Client.new()
    client.name = "Client"
    add_child(client)

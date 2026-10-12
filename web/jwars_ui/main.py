import json
import os
import secrets
import warnings
from datetime import datetime, timezone
from typing import Any

from nicegui import app, ui

from .api import APIError, request


GREEN = "#b7f56d"
DARK = "#101512"


def add_styles() -> None:
    ui.add_head_html("""<style>
      body { background:#101512; color:#e7ece5; font-family:Inter,ui-sans-serif,system-ui,sans-serif; }
      .jwars-page { max-width:1180px; margin:0 auto; padding:24px 28px 70px; }
      .jwars-nav { border-bottom:1px solid #2a342c; padding:16px 28px; display:flex; align-items:center; justify-content:space-between; }
      .jwars-brand { color:#e7ece5; font-weight:800; letter-spacing:.14em; font-size:14px; }
      .jwars-brand b { color:#b7f56d; }
      .jwars-link { color:#a3afa2; font-size:13px; text-decoration:none; }
      .jwars-link:hover { color:#b7f56d; }
      .jwars-card { background:#171e19; border:1px solid #2a342c; border-radius:10px; }
      .jwars-kicker { color:#aeb9aa; text-transform:uppercase; letter-spacing:.14em; font-size:10px; }
      .jwars-title { font-size:clamp(42px,7vw,78px); line-height:1; letter-spacing:-.06em; font-weight:600; }
      .jwars-title em { color:#b7f56d; font-style:normal; }
      .jwars-subtitle { color:#a4afa4; line-height:1.8; max-width:650px; }
      .jwars-green { color:#b7f56d; }
      .jwars-mono { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }
      .jwars-button { background:#b7f56d!important; color:#172011!important; font-weight:700; }
      .jwars-button:hover { background:#d3ff95!important; }
      .q-field--outlined .q-field__control:before { border-color:#3a463b!important; }
      .q-field--focused .q-field__control:after { border-color:#b7f56d!important; }
      .q-field__label,.q-field__native,.q-field__input { color:#e7ece5!important; }
      .q-field__label { color:#9eaa9e!important; }
      .q-table { background:#171e19!important; color:#dce5db!important; }
      pre { white-space:pre-wrap; overflow-wrap:anywhere; background:#121814; color:#c8d3c4; border:1px solid #303a31; border-radius:7px; padding:15px; }
      @media(max-width:600px){.jwars-page{padding:18px 16px 50px}.jwars-nav{padding:14px 16px}}
    </style>""")


def nav(active: str = "") -> None:
    with ui.row().classes("jwars-nav w-full"):
        ui.link("JWARS.", "/").classes("jwars-brand no-underline")
        with ui.row().classes("items-center gap-5"):
            account_path = "/dashboard" if app.storage.user.get("account_token") else "/login"
            for label, path in [("Standings", "/#standings"), ("API guide", "/api"), ("Account", account_path)]:
                ui.link(label, path).classes("jwars-link")
            token = app.storage.user.get("account_token")
            if token:
                ui.link("Dashboard", "/dashboard").classes("jwars-link")
            else:
                ui.link("Sign in", "/login").classes("jwars-link")


def shell(title: str, active: str = "") -> None:
    ui.page_title(f"{title} · JWARS")
    add_styles()
    nav(active)


def show_error(error: Exception) -> None:
    if isinstance(error, APIError):
        ui.notify(str(error), type="negative", position="top")
    else:
        ui.notify("Something went wrong. Please try again.", type="negative", position="top")


def player_name(standing: dict[str, Any]) -> str:
    return standing.get("display_name") or standing.get("player_id", "Unknown commander")


def date_label(value: str | None) -> str:
    if not value:
        return "TBA"
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00")).astimezone(timezone.utc)
        return parsed.strftime("%b %d, %Y %H:%M UTC")
    except ValueError:
        return value


@ui.page("/")
async def home() -> None:
    shell("A world that keeps moving")
    with ui.column().classes("jwars-page gap-10"):
        try:
            board = await request("GET", "/v1/scoreboard")
        except APIError:
            board = None
        season = board.get("season", {}) if board else {}
        with ui.row().classes("w-full items-center justify-between pt-12"):
            ui.label(f"SEASON {season.get('number', '—')}  /  {str(season.get('status', 'WORLD ONLINE')).upper()}").classes("jwars-kicker")
            ui.label("● WORLD ONLINE").classes("jwars-kicker jwars-green")
        ui.html('<div class="jwars-title">A world that<br>keeps <em>moving.</em></div>')
        ui.label("Build your base. Command your forces. Hold the hill. JWARS is a persistent strategy game where every order shapes a living world.").classes("jwars-subtitle text-base")
        with ui.row().classes("items-center gap-4"):
            ui.button("Create your account", on_click=lambda: ui.navigate.to("/register")).classes("jwars-button")
            ui.link("Read the API guide →", "/api").classes("jwars-link")

        with ui.row().classes("w-full gap-4"):
            for value, label in [("7 DAYS", "per season"), ("1 SEC", "world tick"), ("10", "commanders per season"), ("OPEN API", "any language")]:
                with ui.card().classes("jwars-card col p-5"):
                    ui.label(value).classes("text-2xl jwars-green jwars-mono")
                    ui.label(label).classes("text-xs text-grey-5")

        with ui.column().classes("w-full gap-4 pt-6"):
            ui.label("FIELD REPORT / CURRENT SEASON").classes("jwars-kicker")
            ui.html('<div id="standings"></div>')
            with ui.card().classes("jwars-card w-full p-6"):
                with ui.row().classes("w-full items-center justify-between"):
                    ui.label("The hill decides the season").classes("text-xl font-semibold")
                    status = season.get("status", "offline")
                    ui.label(status.upper()).classes("jwars-kicker jwars-green")
                if board and board.get("standings"):
                    with ui.column().classes("w-full gap-0 pt-3"):
                        for rank, standing in enumerate(board["standings"], 1):
                            with ui.row().classes("w-full items-center justify-between border-b border-[#2a342c] py-3"):
                                ui.label(f"{rank:02d}  {player_name(standing)}").classes("text-sm")
                                ui.label(f"{standing.get('score', 0)} PTS").classes("jwars-green jwars-mono text-sm")
                elif board:
                    ui.label("No commanders on the board yet. Be first when enrollment opens.").classes("text-sm text-grey-5 py-8")
                else:
                    ui.label("Standings are temporarily unavailable.").classes("text-sm text-grey-5 py-8")
                if season:
                    ui.label(f"Season ends {date_label(season.get('ends_at'))} · {len(board.get('standings', []))} commanders").classes("jwars-kicker pt-3")

        with ui.row().classes("w-full gap-8 pt-10"):
            for number, title, description in [
                ("01", "Join a season", "Create your account at any time, then enroll during the season window."),
                ("02", "Command your army", "Use the HTTP API with a token from your account dashboard."),
                ("03", "Control the hill", "Hold the center to earn points. The season's top score wins."),
            ]:
                with ui.column().classes("col border-t border-[#3a463b] pt-5"):
                    ui.label(number).classes("jwars-kicker jwars-green")
                    ui.label(title).classes("text-lg font-semibold pt-3")
                    ui.label(description).classes("text-sm text-grey-5 leading-6")

        with ui.card().classes("jwars-card w-full p-8 items-center"):
            ui.label("YOUR COMMAND STARTS HERE").classes("jwars-kicker")
            ui.html('<div class="text-4xl font-semibold text-center pt-2">Ready to enter the <span style="color:#b7f56d">world?</span></div>')
            ui.label("Register, choose your public commander name, and make an API token for your client.").classes("jwars-subtitle text-center")
            ui.button("Create an account", on_click=lambda: ui.navigate.to("/register")).classes("jwars-button")


@ui.page("/register")
def register_page() -> None:
    shell("Create an account")
    with ui.column().classes("jwars-page max-w-xl mx-auto gap-5 pt-12"):
        ui.label("CREATE ACCOUNT / PLAYER ACCESS").classes("jwars-kicker")
        ui.html('<div class="jwars-title text-5xl">Create your<br><em>commander.</em></div>')
        ui.label("No email is collected. Keep your username and password safe; password recovery requires an admin.").classes("jwars-subtitle")
        username = ui.input("Username (private login)").classes("w-full")
        display_name = ui.input("Public display name").classes("w-full")
        password = ui.input("Password (10–128 characters)", password=True, password_toggle_button=True).classes("w-full")
        confirmation = ui.input("Confirm password", password=True, password_toggle_button=True).classes("w-full")

        async def create() -> None:
            if password.value != confirmation.value:
                ui.notify("The passwords do not match.", type="warning")
                return
            try:
                await request("POST", "/v1/accounts", body={"username": username.value, "display_name": display_name.value, "password": password.value})
                session = await request("POST", "/v1/accounts/session", body={"username": username.value, "password": password.value})
                app.storage.user["account_token"] = session["session_token"]
                app.storage.user["account"] = session["account"]
                ui.notify("Account created. Your game world is ready when you join a season.", type="positive")
                ui.navigate.to("/dashboard")
            except APIError as error:
                show_error(error)

        ui.button("Create account", on_click=create).classes("jwars-button")
        with ui.row().classes("items-center gap-2"):
            ui.label("Already have an account?").classes("text-sm text-grey-5")
            ui.link("Sign in", "/login").classes("jwars-green text-sm")


@ui.page("/login")
def login_page() -> None:
    shell("Sign in")
    with ui.column().classes("jwars-page max-w-xl mx-auto gap-5 pt-12"):
        ui.label("PLAYER ACCESS / SIGN IN").classes("jwars-kicker")
        ui.html('<div class="jwars-title text-5xl">Welcome<br><em>back.</em></div>')
        username = ui.input("Username").classes("w-full")
        password = ui.input("Password", password=True, password_toggle_button=True).classes("w-full")

        async def sign_in() -> None:
            try:
                session = await request("POST", "/v1/accounts/session", body={"username": username.value, "password": password.value})
                app.storage.user["account_token"] = session["session_token"]
                app.storage.user["account"] = session["account"]
                ui.navigate.to("/dashboard")
            except APIError as error:
                show_error(error)

        ui.button("Sign in", on_click=sign_in).classes("jwars-button")
        ui.label("Forgot your password? Contact an admin for a temporary password reset.").classes("text-xs text-grey-6")


def get_account_token() -> str | None:
    return app.storage.user.get("account_token")


@ui.page("/dashboard")
async def dashboard() -> None:
    shell("Account dashboard")
    token = get_account_token()
    if not token:
        ui.navigate.to("/login")
        return
    try:
        account = await request("GET", "/v1/account", token=token)
    except APIError:
        app.storage.user.pop("account_token", None)
        app.storage.user.pop("account", None)
        ui.navigate.to("/login")
        return
    app.storage.user["account"] = account
    with ui.column().classes("jwars-page gap-7"):
        with ui.row().classes("w-full items-center justify-between pt-5"):
            with ui.column().classes("gap-1"):
                ui.label("PLAYER ACCOUNT / COMMAND CENTER").classes("jwars-kicker")
                with ui.row().classes("items-baseline gap-2"):
                    ui.label("Welcome,").classes("text-4xl font-semibold")
                    ui.label(account["display_name"]).classes("text-4xl font-semibold jwars-green")
            ui.button("Sign out", on_click=sign_out).props("outline color=white")
        with ui.row().classes("w-full gap-4"):
            for label, value in [("PLAYER ID", account["player_id"]), ("LOGIN USERNAME", account["username"]), ("PUBLIC NAME", account["display_name"])]:
                with ui.card().classes("jwars-card col p-5"):
                    ui.label(label).classes("jwars-kicker")
                    ui.label(value).classes("jwars-mono text-sm pt-2")
        if account.get("must_change_password"):
            await password_panel(token, forced=True)
        else:
            with ui.row().classes("w-full items-start gap-6"):
                with ui.column().classes("col gap-5"):
                    await profile_panel(token, account)
                    await password_panel(token)
                with ui.column().classes("col gap-5"):
                    await season_panel(token, account)
                    await token_panel(token)
            if account.get("is_admin"):
                await admin_panel(token)


async def sign_out() -> None:
    token = get_account_token()
    if token:
        try:
            await request("DELETE", "/v1/account/session", token=token)
        except APIError:
            pass
    app.storage.user.pop("account_token", None)
    app.storage.user.pop("account", None)
    ui.navigate.to("/")


async def profile_panel(token: str, account: dict[str, Any]) -> None:
    with ui.card().classes("jwars-card w-full p-5"):
        ui.label("PUBLIC PROFILE").classes("jwars-kicker")
        name = ui.input("Display name", value=account["display_name"]).classes("w-full pt-3")

        async def save_profile() -> None:
            try:
                updated = await request("PATCH", "/v1/account/profile", token=token, body={"display_name": name.value})
                app.storage.user["account"] = updated
                ui.notify("Display name updated.", type="positive")
            except APIError as error:
                show_error(error)

        ui.button("Save display name", on_click=save_profile).classes("jwars-button")


async def password_panel(token: str, forced: bool = False) -> None:
    with ui.card().classes("jwars-card w-full p-5"):
        ui.label("SET A NEW PASSWORD" if forced else "CHANGE PASSWORD").classes("jwars-kicker")
        if forced:
            ui.label("An admin-issued temporary password was used. Choose a password only you know to continue.").classes("text-sm text-grey-5")
            current = None
        else:
            current = ui.input("Current password", password=True, password_toggle_button=True).classes("w-full pt-3")
        new = ui.input("New password (10–128 characters)", password=True, password_toggle_button=True).classes("w-full")
        confirm = ui.input("Confirm new password", password=True, password_toggle_button=True).classes("w-full")

        async def change() -> None:
            if new.value != confirm.value:
                ui.notify("The new passwords do not match.", type="warning")
                return
            try:
                await request("POST", "/v1/account/password", token=token, body={"current_password": current.value if current else "", "new_password": new.value})
                app.storage.user.pop("account_token", None)
                app.storage.user.pop("account", None)
                ui.notify("Password updated. Sign in with the new password.", type="positive")
                ui.navigate.to("/login")
            except APIError as error:
                show_error(error)

        ui.button("Update password", on_click=change).classes("jwars-button")


async def season_panel(token: str, account: dict[str, Any]) -> None:
    with ui.card().classes("jwars-card w-full p-5"):
        ui.label("CURRENT SEASON").classes("jwars-kicker")
        try:
            season = await request("GET", "/v1/seasons/current")
        except APIError as error:
            ui.label(str(error)).classes("text-sm text-grey-5")
            return
        ui.label(f"Season {season.get('number')} · {season.get('status', '').title()}").classes("text-lg font-semibold pt-2")
        ui.label(f"Enrollment {('open' if season.get('enrollment_open') else 'closed')} · Ends {date_label(season.get('enrollment_ends_at'))}").classes("text-xs text-grey-5")
        if account.get("joined_this_season"):
            ui.label("You are enrolled in this season.").classes("text-sm jwars-green pt-2")
        elif season.get("enrollment_open"):
            async def join() -> None:
                try:
                    result = await request("POST", "/v1/account/join-season", token=token, body={})
                    ui.notify(f"Joined season {result['season']['number']}.", type="positive")
                    ui.navigate.reload()
                except APIError as error:
                    show_error(error)
            ui.button("Join this season", on_click=join).classes("jwars-button mt-3")
        else:
            ui.label("You can join during the next enrollment window.").classes("text-xs text-grey-5 pt-2")


async def token_panel(token: str) -> None:
    with ui.card().classes("jwars-card w-full p-5"):
        ui.label("GAME API TOKENS").classes("jwars-kicker")
        ui.label("Create a token for each client. A token secret is shown once, when created.").classes("text-xs text-grey-5")
        token_name = ui.input("Token name (e.g. Desktop bot)").classes("w-full pt-2")
        tokens_column = ui.column().classes("w-full gap-2")
        reveal = ui.dialog()
        current_secret = {"value": ""}
        with reveal, ui.card().classes("jwars-card max-w-lg"):
            ui.label("COPY YOUR NEW API TOKEN").classes("jwars-kicker")
            ui.label("This token is only shown now. Store it securely; JWARS cannot show it again.").classes("text-sm text-grey-5")
            secret_label = ui.code("").classes("w-full break-all")
            async def copy_current_secret() -> None:
                await ui.run_javascript(f"navigator.clipboard.writeText({json.dumps(current_secret['value'])})")
                ui.notify("Token copied to clipboard.", type="positive")
            ui.button("Copy token", on_click=copy_current_secret).classes("jwars-button")
            ui.button("Done", on_click=reveal.close).props("flat color=white")

        async def refresh_tokens() -> None:
            try:
                result = await request("GET", "/v1/account/api-tokens", token=token)
            except APIError as error:
                show_error(error)
                return
            tokens_column.clear()
            with tokens_column:
                if not result["tokens"]:
                    ui.label("No API tokens created yet.").classes("text-xs text-grey-5 py-2")
                for item in result["tokens"]:
                    with ui.row().classes("w-full items-center justify-between border-b border-[#2a342c] py-2"):
                        with ui.column().classes("gap-0"):
                            ui.label(item["name"]).classes("text-sm")
                            ui.label(f"Created {date_label(item['created_at'])}").classes("jwars-kicker")
                        async def revoke(token_id: str = item["id"], name: str = item["name"]) -> None:
                            async def confirmed() -> None:
                                try:
                                    await request("DELETE", f"/v1/account/api-tokens/{token_id}", token=token)
                                    ui.notify(f"Revoked {name}.", type="positive")
                                    confirm_dialog.close()
                                    await refresh_tokens()
                                except APIError as error:
                                    show_error(error)
                            with ui.dialog() as confirm_dialog, ui.card().classes("jwars-card"):
                                ui.label(f"Revoke token '{name}'?")
                                with ui.row():
                                    ui.button("Cancel", on_click=confirm_dialog.close).props("flat")
                                    ui.button("Revoke", on_click=confirmed).classes("jwars-button")
                            confirm_dialog.open()
                        ui.button("Revoke", on_click=revoke).props("flat dense color=negative")

        async def create_token() -> None:
            try:
                created = await request("POST", "/v1/account/api-tokens", token=token, body={"name": token_name.value})
                secret_label.set_content(created["token"])
                current_secret["value"] = created["token"]
                token_name.value = ""
                reveal.open()
                await refresh_tokens()
            except APIError as error:
                show_error(error)

        with ui.row().classes("w-full items-center"):
            token_name.classes("col")
            ui.button("Create token", on_click=create_token).classes("jwars-button")
        await refresh_tokens()


async def admin_panel(token: str) -> None:
    with ui.card().classes("jwars-card w-full p-6 mt-3"):
        ui.label("ADMIN / PLAYER ACCOUNTS").classes("jwars-kicker")
        ui.label("Temporary passwords appear once after reset. Share them directly with the player.").classes("text-xs text-grey-5")
        accounts_column = ui.column().classes("w-full gap-2 pt-3")
        reset_dialog = ui.dialog()
        with reset_dialog, ui.card().classes("jwars-card max-w-lg"):
            ui.label("TEMPORARY PASSWORD").classes("jwars-kicker")
            ui.label("Share this password directly with the player. They must choose a new one when signing in.").classes("text-sm text-grey-5")
            temporary_password = ui.code("").classes("w-full break-all")
            ui.button("Done", on_click=reset_dialog.close).props("flat color=white")

        async def refresh_accounts() -> None:
            try:
                result = await request("GET", "/v1/admin/accounts", token=token)
            except APIError as error:
                show_error(error)
                return
            accounts_column.clear()
            with accounts_column:
                for player in result["accounts"]:
                    with ui.row().classes("w-full items-center justify-between border-b border-[#2a342c] py-3"):
                        with ui.column().classes("gap-0"):
                            ui.label(f"{player['display_name']} (@{player['username']})").classes("text-sm")
                            ui.label(player["player_id"]).classes("jwars-kicker")
                            if player["suspended"]:
                                ui.label("SUSPENDED").classes("jwars-kicker text-red-4")
                        with ui.row().classes("items-center"):
                            if not player["is_admin"]:
                                async def reset(player_id: str = player["player_id"]) -> None:
                                    try:
                                        result = await request("POST", f"/v1/admin/accounts/{player_id}/password-reset", token=token, body={})
                                        temporary_password.set_content(result["temporary_password"])
                                        reset_dialog.open()
                                    except APIError as error:
                                        show_error(error)
                                async def suspend(player_id: str = player["player_id"], state: bool = player["suspended"]) -> None:
                                    try:
                                        await request("POST", f"/v1/admin/accounts/{player_id}/suspension", token=token, body={"suspended": not state})
                                        await refresh_accounts()
                                    except APIError as error:
                                        show_error(error)
                                ui.button("Reset password", on_click=reset).props("flat dense")
                                ui.button("Unsuspend" if player["suspended"] else "Suspend", on_click=suspend).props("flat dense color=negative")

        await refresh_accounts()


@ui.page("/api")
def api_guide() -> None:
    shell("API guide")
    with ui.column().classes("jwars-page max-w-4xl gap-6 pt-12"):
        ui.label("FIELD MANUAL / API V1").classes("jwars-kicker")
        ui.html('<div class="jwars-title text-6xl">Command the world<br>through <em>code.</em></div>')
        ui.label("Your client sends orders; the server resolves them on its own clock. Create an API token in your player dashboard, then send it as a bearer token.").classes("jwars-subtitle")
        doc_sections = [
            ("ACCOUNT AND ACCESS", "Accounts use a private username/password and a public display name. API tokens are managed in the player dashboard and shown only once. Account-management requests use the session token returned by sign-in.", """POST /v1/accounts
POST /v1/accounts/session

Authorization: Bearer YOUR_SESSION_TOKEN

GET    /v1/account
PATCH  /v1/account/profile
POST   /v1/account/password
POST   /v1/account/api-tokens
DELETE /v1/account/api-tokens/{tokenID}

Game requests use a dashboard API token:
Authorization: Bearer YOUR_API_TOKEN

GET /v1/world
GET /v1/scoreboard
POST /v1/commands"""),
            ("PUBLIC ENDPOINTS", "The current season and scoreboard are public. Account signup is also public and does not automatically join a season.", """GET /v1/seasons/current
GET /v1/scoreboard
POST /v1/accounts"""),
            ("PLAYER ENDPOINTS", "World state, definitions, events, commands, and season enrollment use an API token. The scoreboard includes public display names.", """GET  /v1/world
GET  /v1/definitions/units
GET  /v1/definitions/buildings
GET  /v1/events?after=SEQUENCE
POST /v1/commands
POST /v1/seasons/current/join"""),
            ("COMMANDS", "Commands use a client-generated ID. Repeating the same command ID returns its original result, making retries safe.", """curl -X POST http://localhost:8080/v1/commands \\
  -H 'Authorization: Bearer YOUR_API_TOKEN' \\
  -H 'Content-Type: application/json' \\
  -d '{"id":"order-001","type":"move","unit_ids":["UNIT_ID"],"target":{"x":8,"y":5}}'"""),
            ("LIVE EVENTS", "The event endpoint uses server-sent events. Reconnect with the last sequence number; if it has expired, the server sends a fresh snapshot.", """curl -N -H 'Authorization: Bearer YOUR_API_TOKEN' \\
  'http://localhost:8080/v1/events?after=0'"""),
            ("SEASONS", "Seasons start Monday at 18:00 UTC. Enrollment lasts 24 hours or closes at the player cap. Returning players keep their player ID and API tokens, but must join during each enrollment window.", "See gameloop.md and README.md in the repository for season timing and game mechanics."),
        ]
        for heading, description, sample in doc_sections:
            with ui.card().classes("jwars-card w-full p-6"):
                ui.label(heading).classes("jwars-kicker")
                ui.label(description).classes("text-sm text-grey-4 leading-7 pt-2")
                if sample:
                    ui.code(sample).classes("w-full mt-3")


def run() -> None:
    storage_secret = os.getenv("JWARS_UI_SESSION_SECRET")
    if not storage_secret:
        if os.getenv("JWARS_ENV", "development").lower() == "production":
            raise RuntimeError("JWARS_UI_SESSION_SECRET must be set in production")
        storage_secret = secrets.token_urlsafe(48)
        warnings.warn("JWARS_UI_SESSION_SECRET is not set; existing browser sessions will be signed out after a restart.")
    https_only = os.getenv("JWARS_UI_HTTPS_ONLY", "false").lower() in {"1", "true", "yes"}
    app.colors(primary=GREEN, dark=DARK)
    ui.run(
        title="JWARS",
        favicon="✦",
        host=os.getenv("JWARS_UI_HOST", "127.0.0.1"),
        port=int(os.getenv("JWARS_UI_PORT", "8081")),
        storage_secret=storage_secret,
        session_middleware_kwargs={"https_only": https_only, "same_site": "lax"},
        dark=True,
        reload=False,
    )

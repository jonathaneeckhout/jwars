import os
from typing import Any

import httpx


API_ORIGIN = os.getenv("JWARS_API_ORIGIN", "http://127.0.0.1:8080").rstrip("/")


class APIError(Exception):
    def __init__(self, status_code: int, message: str):
        super().__init__(message)
        self.status_code = status_code


async def request(
    method: str,
    path: str,
    *,
    token: str | None = None,
    body: dict[str, Any] | None = None,
) -> Any:
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    try:
        async with httpx.AsyncClient(base_url=API_ORIGIN, timeout=10.0) as client:
            response = await client.request(method, path, headers=headers, json=body)
    except httpx.HTTPError as error:
        raise APIError(503, "The game service is unavailable right now.") from error
    if response.is_error:
        try:
            message = response.json().get("error", "Request failed.")
        except (ValueError, AttributeError):
            message = "Request failed."
        raise APIError(response.status_code, message)
    if response.status_code == 204:
        return None
    return response.json()

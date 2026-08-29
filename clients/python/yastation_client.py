"""yastation_client.py — тонкая обёртка над HTTP-вебхуком yastation-server.

Ноль внешних зависимостей (только stdlib), работает на любом Python 3.9+.
Копируй файл к себе в проект или запускай напрямую как пример:

    python3 yastation_client.py

Переменные окружения для примера в конце файла:
    YASTATION_URL          адрес сервера, по умолчанию http://localhost:8737
    MOM_YANDEX_TOKEN       опционально — x-token другого аккаунта для
                            демонстрации bring-your-own-token режима
"""
from __future__ import annotations

import json
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any, Optional


class YastationError(RuntimeError):
    """Сетевая ошибка, HTTP-ошибка или ok=false в ответе yastation-server."""


@dataclass
class Station:
    id: str
    name: str
    house_name: Optional[str] = None
    platform: Optional[str] = None


class YastationClient:
    """Клиент к HTTP API yastation-server (см. README проекта, раздел
    "HTTP-бэкенд").

    yandex_token — режим "bring your own token": выполнять команды от
        имени конкретного аккаунта Яндекса (заголовок X-Yandex-Token),
        а не от аккаунта, под которым авторизован сам сервер. Нужен для
        stations() и для команд на чужую колонку (см. README, раздел
        "Bring your own token").
    """

    def __init__(
        self,
        base_url: str,
        yandex_token: Optional[str] = None,
        timeout: float = 15.0,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.yandex_token = yandex_token
        self.timeout = timeout

    # --- низкий уровень ---------------------------------------------

    def _headers(self, extra: Optional[dict] = None) -> dict:
        headers = {"Content-Type": "application/json"}
        if self.yandex_token:
            headers["X-Yandex-Token"] = self.yandex_token
        if extra:
            headers.update(extra)
        return headers

    def _request(
        self,
        method: str,
        path: str,
        body: Optional[dict] = None,
        headers: Optional[dict] = None,
    ) -> Any:
        url = f"{self.base_url}{path}"
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = urllib.request.Request(url, data=data, method=method, headers=self._headers(headers))
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read().decode("utf-8")
        except urllib.error.HTTPError as e:
            raw = e.read().decode("utf-8")
            try:
                message = json.loads(raw).get("error", raw)
            except (json.JSONDecodeError, AttributeError):
                message = raw
            raise YastationError(f"HTTP {e.code}: {message}") from e
        except urllib.error.URLError as e:
            raise YastationError(f"не достучался до {url}: {e.reason}") from e

        if not raw:
            return None
        return json.loads(raw)

    @staticmethod
    def _raise_if_error(result: dict) -> None:
        if not result.get("ok"):
            raise YastationError(result.get("error", "неизвестная ошибка"))

    # --- высокий уровень ----------------------------------------------

    def command(self, line: str, station: Optional[str] = None) -> str:
        """Выполнить полную команду REPL, например '/volume 3' или
        '/every 30m /say вода'."""
        headers = {"X-Station": station} if station else None
        result = self._request("POST", "/command", {"line": line}, headers)
        self._raise_if_error(result)
        return result.get("output", "")

    def say(self, text: str, station: Optional[str] = None) -> str:
        """Озвучить текст через TTS (как /say)."""
        return self._text_command(text, station, as_command=False)

    def ask(self, text: str, station: Optional[str] = None) -> str:
        """Отправить текст как голосовую команду Алисе (как /cmd или /ask —
        ответ прозвучит из колонки, сюда не вернётся)."""
        return self._text_command(text, station, as_command=True)

    def _text_command(self, text: str, station: Optional[str], as_command: bool) -> str:
        body: dict = {"text": text, "as_command": as_command}
        if station:
            body["station"] = station
        result = self._request("POST", "/command", body)
        self._raise_if_error(result)
        return result.get("output", "")

    def stations(self) -> list[Station]:
        """Список колонок аккаунта, привязанного к yandex_token
        (bring-your-own-token режим — без yandex_token сервер не знает,
        чей аккаунт спрашивать)."""
        if not self.yandex_token:
            raise YastationError("stations() требует yandex_token (bring-your-own-token режим)")
        result = self._request("GET", "/stations")
        return [Station(**item) for item in result]

    def schedules(self) -> list[dict]:
        """Список активных запланированных команд (/every, /at) сервера."""
        return self._request("GET", "/schedules")

    def healthy(self) -> bool:
        url = f"{self.base_url}/healthz"
        req = urllib.request.Request(url, method="GET")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                return resp.status == 200
        except (urllib.error.HTTPError, urllib.error.URLError):
            return False


if __name__ == "__main__":
    import os

    client = YastationClient(
        base_url=os.environ.get("YASTATION_URL", "http://localhost:8737"),
    )

    if not client.healthy():
        raise SystemExit(f"yastation-server недоступен на {client.base_url}")

    print(client.say("привет с питона"))
    print(client.command("/volume 3"))
    print(client.ask("какая погода"))

    for task in client.schedules():
        print("запланировано:", task["spec"], "->", task["command_line"])

    # Bring-your-own-token: управление колонкой другого аккаунта, например
    # маминой. Работает через тот же сервер, но с ЕЁ x-token в заголовке —
    # см. README, раздел "Bring your own token", про то, что это за токен
    # и кому его можно доверять.
    mom_token = os.environ.get("MOM_YANDEX_TOKEN")
    if mom_token:
        mom_client = YastationClient(
            base_url=client.base_url,
            yandex_token=mom_token,
        )
        for station in mom_client.stations():
            print(station.id, station.name, station.house_name)
        mom_client.say("мама, не забудь лекарство", station="Кухня")

"""Точка входа: python -m yastation

Подключается к станции и запускает интерактивный цикл.
Свободный текст без "/" произносится станцией. Команды начинаются с "/",
например /volume 0.3, /timer 10 проверить духовку, /stations.
Наберите /help для списка команд, Ctrl+C или пустой ввод (EOF) — выход.
"""
from __future__ import annotations

import asyncio
import sys

from func_parser import ExecutionContext, User

from .commands import parser
from .station_client import close_client, with_client


async def main() -> None:
    print("Подключаюсь к Яндекс Станции...")
    try:
        client = await with_client()
    except Exception as exc:  # первый запуск без токена и т.п.
        print(f"Не удалось подключиться: {exc}")
        print("Если это первый запуск — авторизуйтесь: uv run yastation-auth")
        sys.exit(1)

    speaker = client.select_speaker()
    print(f"Подключено. Станция по умолчанию: {speaker.get('name')}")
    print("Пиши текст — он будет озвучен станцией. Команды — с /, /help — список.")

    ctx = ExecutionContext(user=User(id="local", name="deniz", roles=["admin"]))
    ctx.set_var("client", client)

    try:
        async for result in parser.async_loop(prompt="> ", ctx=ctx):
            if result.status == "success":
                if result.output:
                    print(result.output)
            elif result.status == "unknown":
                print(f"Неизвестная команда: {result.name}. Наберите /help.")
            else:
                print(f"Ошибка ({result.status}): {result.error or result.output}")
    finally:
        await close_client(client)
        print("Отключено.")


def run() -> None:
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    run()

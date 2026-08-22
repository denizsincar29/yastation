from yastation_client import YastationClient as Client

token = "REDACTED_SERVER_TOKEN"
yatoken = "REDACTED_YANDEX_TOKEN"

c = Client("https://station.denizsincar.ru", token, yandex_token=yatoken)
c.ask("Громкость 10")
c.say("Яндекс рулит!")
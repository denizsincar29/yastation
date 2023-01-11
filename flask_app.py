from flask import Flask
from flask import request
import json

app = Flask(__name__)

@app.route('/post', methods=['POST'])
def main():
    ## Создаем ответ
    response = {
        'session': request.json['session'],
        'version': request.json['version'],
        'response': {
            'end_session': False
        }
    }
    ## Заполняем необходимую информацию
    handle_dialog(response, request.json)
    return json.dumps(response)


def handle_dialog(res,req):
    if req['request']['original_utterance']:
        ## Проверяем, есть ли содержимое
        res['response']['text'], res['response']['end_session'] = dialog(req['request']['original_utterance'])

    else:
        ## Если это первое сообщение — представляемся
        res['response']['text'] = "привет. Я- тестовый навык написанный на питоне. знаю, что криворуко накодили меня, но всё же я полноценный скилл. я пока умею только повторять за тобой."


def dialog(text):
	if "до свидания" in text or "пока" in text or "закрой" in text or "выключи" in text:
		return ("ты меня вырубил.", True)
	else:
		return (text, False)
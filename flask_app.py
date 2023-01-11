from flask import Flask
from flask import request
import requests
import json
import git 

g = git.cmd.Git("hello_world_alice")
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

@app.route("/update")
def update():
	g.pull()
	username = "voicechat"
	api_token = "b2cc50d42379ca2216a0dde6a4f202196966722d"
	domain_name="voicechat.pythonanywhere.com"

	response = requests.post(
		'https://www.pythonanywhere.com/api/v0/user/{username}/webapps/{domain_name}/reload/'.format(
			username=username, domain_name=domain_name
		),
		headers={'Authorization': 'Token {token}'.format(token=api_token)}
	)
	if response.status_code == 200:
		return ('reloaded OK')
	else:
		return str('Got unexpected status code {}: {!r}'.format(response.status_code, response.content))

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
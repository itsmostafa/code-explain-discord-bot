import random


def handle_response(message) -> str:
    p_message = message.lower()

    if p_message == "hello":
        return "Hello! How can I help you today?"

    if p_message == "goodbye":
        return str(random.randint(1, 6))

    if p_message == "!help":
        return "`This is a help message that you can modify`"

    return "I don't know what you said."

import os

import discord
from discord.message import Message
from openai.types.beta.threads.run import Run

from services.chatgpt.assistant import (
    create_message,
    create_thread,
    get_assistant_response,
    retrieve_thread,
    run_thread,
)
from services.chatgpt.assistants.sse import sse_assistant
import services.discord.responses as responses


TOKEN = os.getenv("DISCORD_TOKEN", "")


async def send_message(message: Message, response: str, is_private):
    try:
        (
            await message.author.send(response)
            if is_private
            else await message.channel.send(response)
        )
    except Exception as e:
        print(e)


def run_discord_bot():
    client = discord.Client(intents=discord.Intents.default())
    thread_id: str = ""
    run_new_thread: Run

    @client.event
    async def on_ready():
        nonlocal thread_id, run_new_thread
        thread_id = create_thread()
        run_new_thread = run_thread(sse_assistant().id, thread_id)
        print(f"We have logged in as {client.user}")

    @client.event
    async def on_message(message):
        if message.author == client.user:
            return

        username = str(message.author)
        user_message = str(message.content)
        channel = str(message.channel)

        print(f"{username} said: {user_message} in {channel}")

        if user_message[0] == "?":
            user_message = user_message[1:]
            await send_message(message, user_message, is_private=True)
        else:
            create_message(user_message, thread_id)
            retrieve_thread(thread_id, run_new_thread)
            assistant_response = get_assistant_response(thread_id)
            await send_message(message, assistant_response, is_private=False)

    client.run(TOKEN)

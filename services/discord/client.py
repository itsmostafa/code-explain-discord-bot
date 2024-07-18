import os

import discord

from services.discord import HELP_MESSAGE
from services.openai.chatgpt import chatgpt_response


DISCORD_TOKEN = os.getenv("DISCORD_TOKEN", "")


class DiscordClient(discord.Client):

    def __init__(self):
        """Initialize the Discord client."""
        super().__init__(intents=discord.Intents.default())
        self.intents.message_content = True

    async def on_ready(self):
        """Print a message when the bot is ready."""
        print(f"Logged on as {self.user}!")

    async def on_message(self, message: discord.Message):
        """Respond to a message."""
        print(f"Message from {message.author}: {message.content}")

        if message.author == self.user:
            return

        is_command = False
        user_message = message.content

        if message.content.startswith("/help"):
            is_command = True
            await self._send_message(
                message,
                response=HELP_MESSAGE,
            )
            return

        if message.content.startswith("/ai"):
            is_command = True
            user_message = message.content.replace("/ai", "")

        if isinstance(message.channel, discord.DMChannel) or is_command:
            async with message.channel.typing():
                response = chatgpt_response(prompt=user_message)
                await self._send_message(message, response)

    async def _send_message(self, message: discord.Message, response) -> None:
        """Send a message to the channel."""
        if len(response) > 2000:
            for i in range(0, len(response), 2000):
                await message.channel.send(response[i : i + 2000])
                return

        await message.channel.send(response)

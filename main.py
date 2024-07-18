from services.discordpy.client import DiscordClient, DISCORD_TOKEN

if __name__ == "__main__":
    client = DiscordClient()
    client.run(DISCORD_TOKEN)

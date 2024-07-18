import openai


def chatgpt_response(prompt) -> str:
    response = openai.chat.completions.create(
        model="gpt-4o",
        messages=[
            {
                "role": "system",
                "content": "You are a helpful assistant.",
            },
            {
                "role": "user",
                "content": prompt,
            },
        ],
    )

    if not response.choices:
        return "Sorry, I don't understand."

    return response.choices[0].message.content

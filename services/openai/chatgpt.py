import openai

MESSAGES = [
    {
        "role": "system",
        "content": (
            "You are an expert in programming. Answer questions related to programming only."
            "If the question is not related to programming, respond with 'I can only answer questions related to programming. How can I help you?'"
        ),
    }
]


def chatgpt_response(prompt) -> str:
    response = openai.chat.completions.create(
        model="gpt-4o",
        messages=[
            {
                "role": "system",
                "content": (
                    "You are an expert in programming. Answer questions related to programming only."
                    "If the question is not related to programming, respond with 'I can only answer questions related to programming. How can I help you?'"
                ),
            },
            {
                "role": "user",
                "content": prompt,
            },
        ],
    )

    if not response.choices or type(response.choices[0].message) is not str:
        return "Sorry, I don't understand."

    return response.choices[0].message.content

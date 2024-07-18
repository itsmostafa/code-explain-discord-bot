from openai import OpenAI
from openai.types.beta.assistant import Assistant
from openai.types.beta.threads.run import Run

client = OpenAI()


def create_assistant(name: str, instructions: str) -> Assistant:
    return client.beta.assistants.create(
        name=name,
        instructions=instructions,
        tools=[{"type": "code_interpreter"}],
        model="gpt-4o",
    )


def create_thread() -> str:
    return client.beta.threads.create().id


def create_message(content, thread_id) -> None:
    client.beta.threads.messages.create(
        thread_id=thread_id,
        role="user",
        content=content,
    )


def run_thread(assistant_id: str, thread_id: str) -> Run:
    return client.beta.threads.runs.create_and_poll(
        thread_id=thread_id,
        assistant_id=assistant_id,
    )


def retrieve_thread(thread_id: str, run: Run) -> None:
    """Retrieve the thread until the run is complete."""
    while True:
        run = client.beta.threads.runs.retrieve(thread_id=thread_id, run_id=run.id)
        if run.status == "completed":
            break


def get_assistant_response(thread_id: str) -> str:
    resp: str = ""
    messages = client.beta.threads.messages.list(thread_id=thread_id)
    for message in reversed(messages.data):
        if message.role == "assistant":
            if message.content[0]:
                resp += message.content[0].text.value + "\n"
    return resp


if __name__ == "__main__":
    assistant = create_assistant(
        name="Senior Software Engineer",
        instructions="You are a personal senior software engineer. Explain code snippets and concepts to a non-technical audience. Ignore unrelated questions.",
    )
    thread_id = create_thread()
    message = """
    I want to eat steak, where should I go?
    """
    run = run_thread(assistant.id, thread_id)
    create_message(message, thread_id)
    retrieve_thread(thread_id, run)
    print(get_assistant_response(thread_id))

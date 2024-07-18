from openai.types.beta.assistant import Assistant

from services.openai.assistant import create_assistant


def sse_assistant() -> Assistant:
    """Create a personal senior software engineer assistant."""
    return create_assistant(
        name="Senior Software Engineer",
        instructions="You are a personal senior software engineer. Explain code snippets and concepts to a non-technical audience. Ignore unrelated questions.",
    )

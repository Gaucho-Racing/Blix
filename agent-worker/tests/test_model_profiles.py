import unittest
from unittest.mock import patch

from blix_agent_worker.llm.openai_responses import OpenAIResponsesModel
from blix_agent_worker.models.llm import ModelMessage, ModelMessageRole, ModelRequest


class ModelProfileTests(unittest.IsolatedAsyncioTestCase):
    def request(self):
        return ModelRequest(messages=(ModelMessage(role=ModelMessageRole.USER, content="Test question"),))

    def environment(self):
        return {
            "LLM_GATEWAY_ENDPOINT": "https://gateway.example/v1",
            "LLM_GATEWAY_API_KEY": "test-key",
            "LLM_LEAD_MODEL": "lead-model",
            "LLM_LEAD_REASONING_EFFORT": "medium",
            "LLM_RESEARCH_MODEL": "research-model",
            "LLM_RESEARCH_REASONING_EFFORT": "high",
            "LLM_CURATOR_MODEL": "curator-model",
            "LLM_CURATOR_REASONING_EFFORT": "medium",
            "LLM_DOCUMENT_QA_MODEL": "question-model",
            "LLM_DOCUMENT_QA_REASONING_EFFORT": "low",
            "LLM_MODEL": "unrelated-global-model",
            "LLM_REASONING_EFFORT": "max",
        }

    async def test_each_profile_sends_its_own_model_and_reasoning_effort(self):
        expected = {
            "lead": ("lead-model", "medium"),
            "research": ("research-model", "high"),
            "curator": ("curator-model", "medium"),
            "document_qa": ("question-model", "low"),
        }
        with patch.dict("os.environ", self.environment(), clear=True):
            for profile, (model_name, effort) in expected.items():
                with self.subTest(profile=profile):
                    async with OpenAIResponsesModel.from_environment(profile) as model:
                        request = self.request()
                        payload = model._request_payload(request, model._input_items(request))
                        self.assertEqual(payload["model"], model_name)
                        self.assertEqual(payload["reasoning"], {"effort": effort})

    async def test_missing_or_blank_profile_settings_do_not_use_global_settings(self):
        for prefix in ("LEAD", "RESEARCH", "CURATOR", "DOCUMENT_QA"):
            for suffix in ("MODEL", "REASONING_EFFORT"):
                key = f"LLM_{prefix}_{suffix}"
                for value in (None, "", "  "):
                    with self.subTest(key=key, value=value):
                        environment = self.environment()
                        if value is None:
                            del environment[key]
                        else:
                            environment[key] = value
                        with patch.dict("os.environ", environment, clear=True):
                            with self.assertRaisesRegex(ValueError, key):
                                OpenAIResponsesModel.from_environment(prefix.lower())

    async def test_unknown_profile_cannot_silently_use_lead(self):
        with patch.dict("os.environ", self.environment(), clear=True):
            with self.assertRaisesRegex(ValueError, "Unknown model profile"):
                OpenAIResponsesModel.from_environment("unknown")

    async def test_invalid_reasoning_effort_identifies_its_profile_setting(self):
        environment = self.environment()
        environment["LLM_RESEARCH_REASONING_EFFORT"] = "invalid"
        with patch.dict("os.environ", environment, clear=True):
            with self.assertRaisesRegex(ValueError, "LLM_RESEARCH_REASONING_EFFORT is invalid"):
                OpenAIResponsesModel.from_environment("research")

    async def test_profile_settings_are_trimmed_and_none_is_an_explicit_effort(self):
        environment = self.environment()
        environment["LLM_DOCUMENT_QA_MODEL"] = "  question-model  "
        environment["LLM_DOCUMENT_QA_REASONING_EFFORT"] = "  none  "
        with patch.dict("os.environ", environment, clear=True):
            async with OpenAIResponsesModel.from_environment("document_qa") as model:
                request = self.request()
                payload = model._request_payload(request, model._input_items(request))
                self.assertEqual(payload["model"], "question-model")
                self.assertEqual(payload["reasoning"], {"effort": "none"})

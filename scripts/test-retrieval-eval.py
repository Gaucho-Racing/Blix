import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("retrieval_eval", Path(__file__).with_name("eval-retrieval.py"))
eval_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(eval_module)


class EvaluationTests(unittest.TestCase):
    def test_page_hit_requires_matching_document(self):
        result = {"document": "sae", "expected_pages": [24], "retrieval": {"results": [{"document_id": "gr25-id", "pdf_page": 24}, {"document_id": "sae-id", "pdf_page": 24}]}}
        eval_module.score_pages(result, {"sae": "sae-id", "gr25": "gr25-id"})
        self.assertFalse(result["hit_at_1"])
        self.assertTrue(result["hit_at_5"])
        self.assertEqual(result["reciprocal_rank"], .5)

    def test_answer_checks_reject_unretrieved_citations_and_guesses(self):
        result = {"document": "sae", "expected_pages": [24], "expect_abstention": False, "answer_patterns": ["350"], "retrieval": {"results": [{"document_id": "sae-id", "pdf_page": 24}]}}
        answer = {"answer": "350 mm", "citations": [{"document": "sae-id", "page": 24}], "abstained": False}
        self.assertTrue(all(eval_module.check_answer(result, answer, {"sae": "sae-id"}).values()))
        answer["citations"][0]["page"] = 25
        self.assertFalse(eval_module.check_answer(result, answer, {"sae": "sae-id"})["citation_provenance_pass"])
        result["expect_abstention"] = True
        self.assertFalse(eval_module.check_answer(result, answer, {"sae": "sae-id"})["expected_behavior_pass"])


if __name__ == "__main__":
    unittest.main()

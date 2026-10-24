//! Typed yes/no decisions for the opt-in Jev intent lane.
//!
//! Providers receive bounded, indexed excerpts only. This slice deliberately ships a
//! fixture provider; network transport is not part of the implementation.

use serde::Serialize;
use std::collections::BTreeMap;
use std::path::Path;

pub const MAX_CANDIDATE_FILES: usize = 32;
pub const MAX_DECLARATIONS: usize = 64;
pub const MAX_EXCERPT_BYTES: usize = 8 * 1024;
pub const MAX_BATCH_BYTES: usize = 64 * 1024;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CandidateKind {
    File,
    Declaration,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Candidate {
    pub id: String,
    pub kind: CandidateKind,
    pub excerpt: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Question {
    pub id: String,
    pub text: String,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Decision {
    pub question_id: String,
    pub probabilities: BTreeMap<String, f64>,
}

#[derive(Debug)]
pub struct BatchDecision {
    pub decision: Decision,
    pub complete: bool,
    pub error: Option<ProviderError>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ProviderError {
    InvalidQuestion,
    QuestionTooLarge,
    InvalidCandidates,
    QuestionMismatch,
    CandidateMismatch,
    InvalidProbability,
    Fixture(String),
}

/// A provider classifies one bounded batch against one typed yes/no question.
pub trait DecisionProvider: std::fmt::Debug {
    fn decide(
        &self,
        question: &Question,
        candidates: &[Candidate],
    ) -> Result<Decision, ProviderError>;
}

/// Reads recorded decisions from JSON shaped as `{question-id: {candidate-id: probability}}`.
#[derive(Debug)]
pub struct FixtureProvider {
    fixtures: BTreeMap<String, BTreeMap<String, f64>>,
}

impl FixtureProvider {
    pub fn from_json(json: &str) -> Result<Self, ProviderError> {
        let fixtures =
            serde_json::from_str(json).map_err(|e| ProviderError::Fixture(e.to_string()))?;
        Ok(Self { fixtures })
    }

    pub fn from_file(path: &Path) -> Result<Self, ProviderError> {
        let json =
            std::fs::read_to_string(path).map_err(|e| ProviderError::Fixture(e.to_string()))?;
        Self::from_json(&json)
    }
}

impl DecisionProvider for FixtureProvider {
    fn decide(
        &self,
        question: &Question,
        candidates: &[Candidate],
    ) -> Result<Decision, ProviderError> {
        validate_request(question, candidates)?;
        let probabilities = self
            .fixtures
            .get(&question.id)
            .ok_or(ProviderError::QuestionMismatch)?;
        let mut result = BTreeMap::new();
        for candidate in candidates {
            let probability = probabilities
                .get(&candidate.id)
                .copied()
                .ok_or(ProviderError::CandidateMismatch)?;
            if !probability.is_finite() || !(0.0..=1.0).contains(&probability) {
                return Err(ProviderError::InvalidProbability);
            }
            result.insert(candidate.id.clone(), probability);
        }
        let decision = Decision {
            question_id: question.id.clone(),
            probabilities: result,
        };
        validate_decision(question, candidates, &decision)?;
        Ok(decision)
    }
}

/// Validates both sides of the provider boundary for every implementation.
pub fn decide(
    provider: &dyn DecisionProvider,
    question: &Question,
    candidates: &[Candidate],
) -> Result<Decision, ProviderError> {
    validate_request(question, candidates)?;
    let decision = provider.decide(question, candidates)?;
    validate_decision(question, candidates, &decision)?;
    Ok(decision)
}

/// Classifies bounded sequential batches, retaining valid decisions if later coverage fails.
pub fn decide_batched(
    provider: &dyn DecisionProvider,
    question: &Question,
    candidates: &[Candidate],
) -> BatchDecision {
    let mut batches = Vec::new();
    let mut batch = Vec::new();
    let mut complete = true;
    let mut error = validate_request(question, &[]).err();
    if error.is_some() {
        return BatchDecision {
            decision: Decision {
                question_id: question.id.clone(),
                probabilities: BTreeMap::new(),
            },
            complete: false,
            error,
        };
    }
    for candidate in candidates {
        let mut proposed = batch.clone();
        proposed.push(candidate.clone());
        if validate_request(question, &proposed).is_ok() {
            batch = proposed;
            continue;
        }
        if !batch.is_empty() {
            batches.push(std::mem::take(&mut batch));
        }
        if validate_request(question, std::slice::from_ref(candidate)).is_ok() {
            batch.push(candidate.clone());
        } else {
            complete = false;
            error.get_or_insert(ProviderError::InvalidCandidates);
        }
    }
    if !batch.is_empty() {
        batches.push(batch);
    }

    let mut probabilities = BTreeMap::new();
    for batch in batches {
        match decide(provider, question, &batch) {
            Ok(decision) => probabilities.extend(decision.probabilities),
            Err(failure) => {
                complete = false;
                error = Some(failure);
                break;
            }
        }
    }
    BatchDecision {
        decision: Decision {
            question_id: question.id.clone(),
            probabilities,
        },
        complete,
        error,
    }
}

/// Refuses incomplete, mismatched, or out-of-range provider output before it can be used.
pub fn validate_decision(
    question: &Question,
    candidates: &[Candidate],
    decision: &Decision,
) -> Result<(), ProviderError> {
    if decision.question_id != question.id {
        return Err(ProviderError::QuestionMismatch);
    }
    if decision.probabilities.len() != candidates.len()
        || candidates
            .iter()
            .any(|candidate| !decision.probabilities.contains_key(&candidate.id))
    {
        return Err(ProviderError::CandidateMismatch);
    }
    if decision
        .probabilities
        .values()
        .any(|probability| !probability.is_finite() || !(0.0..=1.0).contains(probability))
    {
        return Err(ProviderError::InvalidProbability);
    }
    Ok(())
}

/// Enforces the serialized request bounds before any provider implementation sees input.
pub fn validate_request(
    question: &Question,
    candidates: &[Candidate],
) -> Result<(), ProviderError> {
    if question.id.is_empty() || question.text.trim().is_empty() {
        return Err(ProviderError::InvalidQuestion);
    }
    if question.id.len().saturating_add(question.text.len()) > MAX_BATCH_BYTES {
        return Err(ProviderError::QuestionTooLarge);
    }
    if candidates.len() > MAX_CANDIDATE_FILES + MAX_DECLARATIONS {
        return Err(ProviderError::InvalidCandidates);
    }
    let batch_bytes = candidates.iter().fold(
        question.id.len() + question.text.len(),
        |total, candidate| {
            total
                .saturating_add(candidate.id.len())
                .saturating_add(candidate.excerpt.len())
        },
    );
    if candidates
        .iter()
        .any(|c| c.id.is_empty() || c.excerpt.len() > MAX_EXCERPT_BYTES)
        || candidates
            .iter()
            .filter(|c| c.kind == CandidateKind::File)
            .count()
            > MAX_CANDIDATE_FILES
        || candidates
            .iter()
            .filter(|c| c.kind == CandidateKind::Declaration)
            .count()
            > MAX_DECLARATIONS
        || batch_bytes > MAX_BATCH_BYTES
    {
        return Err(ProviderError::InvalidCandidates);
    }
    let serialized = serde_json::to_vec(&(question, candidates))
        .map_err(|_| ProviderError::InvalidCandidates)?;
    if serialized.len() > MAX_BATCH_BYTES {
        return Err(ProviderError::InvalidCandidates);
    }
    let mut ids: Vec<&str> = candidates.iter().map(|c| c.id.as_str()).collect();
    ids.sort_unstable();
    if ids.windows(2).any(|pair| pair[0] == pair[1]) {
        return Err(ProviderError::InvalidCandidates);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn question() -> Question {
        Question {
            id: "q1".into(),
            text: "Does this implement the behavior?".into(),
        }
    }

    fn candidate(id: &str) -> Candidate {
        Candidate {
            id: id.into(),
            kind: CandidateKind::File,
            excerpt: "bounded indexed excerpt".into(),
        }
    }

    #[test]
    fn fixture_returns_one_probability_for_each_requested_candidate() {
        let provider = FixtureProvider::from_json(r#"{"q1":{"a":0.75,"b":0.25}}"#).unwrap();
        let decision = provider
            .decide(&question(), &[candidate("a"), candidate("b")])
            .unwrap();
        assert_eq!(decision.question_id, "q1");
        assert_eq!(decision.probabilities["a"], 0.75);
        assert_eq!(decision.probabilities["b"], 0.25);
    }

    #[test]
    fn fixture_can_answer_a_bounded_stage_from_a_larger_recording() {
        let provider = FixtureProvider::from_json(r#"{"q1":{"a":0.75,"b":0.25}}"#).unwrap();
        let decision = provider.decide(&question(), &[candidate("a")]).unwrap();
        assert_eq!(decision.probabilities, BTreeMap::from([("a".into(), 0.75)]));
    }

    #[test]
    fn decision_validation_refuses_mismatched_or_incomplete_provider_output() {
        let candidates = [candidate("a"), candidate("b")];
        let valid = Decision {
            question_id: "q1".into(),
            probabilities: BTreeMap::from([("a".into(), 0.5), ("b".into(), 1.0)]),
        };
        assert_eq!(validate_decision(&question(), &candidates, &valid), Ok(()));
        assert_eq!(
            validate_decision(
                &question(),
                &candidates,
                &Decision {
                    question_id: "other".into(),
                    ..valid.clone()
                }
            ),
            Err(ProviderError::QuestionMismatch)
        );
        assert_eq!(
            validate_decision(
                &question(),
                &candidates,
                &Decision {
                    probabilities: BTreeMap::from([("a".into(), 0.5), ("extra".into(), 0.5)]),
                    ..valid.clone()
                }
            ),
            Err(ProviderError::CandidateMismatch)
        );
        assert_eq!(
            validate_decision(
                &question(),
                &candidates,
                &Decision {
                    probabilities: BTreeMap::from([("a".into(), f64::NAN), ("b".into(), 0.5)]),
                    ..valid
                }
            ),
            Err(ProviderError::InvalidProbability)
        );
    }

    #[test]
    fn mismatched_question_candidate_and_out_of_range_probability_are_refused() {
        let provider = FixtureProvider::from_json(r#"{"q1":{"a":1.1}}"#).unwrap();
        assert_eq!(
            provider.decide(
                &Question {
                    id: "missing".into(),
                    ..question()
                },
                &[candidate("a")],
            ),
            Err(ProviderError::QuestionMismatch)
        );
        assert_eq!(
            provider.decide(&question(), &[candidate("missing")]),
            Err(ProviderError::CandidateMismatch)
        );
        assert_eq!(
            provider.decide(&question(), &[candidate("a")]),
            Err(ProviderError::InvalidProbability)
        );
    }

    #[derive(Debug)]
    struct InvalidResponseProvider(Decision);

    impl DecisionProvider for InvalidResponseProvider {
        fn decide(
            &self,
            _question: &Question,
            _candidates: &[Candidate],
        ) -> Result<Decision, ProviderError> {
            Ok(self.0.clone())
        }
    }

    #[test]
    fn shared_decide_validates_non_fixture_provider_responses() {
        let candidates = [candidate("a")];
        for (decision, expected) in [
            (
                Decision {
                    question_id: "wrong".into(),
                    probabilities: BTreeMap::from([("a".into(), 0.5)]),
                },
                ProviderError::QuestionMismatch,
            ),
            (
                Decision {
                    question_id: "q1".into(),
                    probabilities: BTreeMap::from([("extra".into(), 0.5)]),
                },
                ProviderError::CandidateMismatch,
            ),
            (
                Decision {
                    question_id: "q1".into(),
                    probabilities: BTreeMap::from([("a".into(), 1.5)]),
                },
                ProviderError::InvalidProbability,
            ),
        ] {
            let provider = InvalidResponseProvider(decision);
            assert_eq!(decide(&provider, &question(), &candidates), Err(expected));
        }
    }

    #[derive(Debug, Default)]
    struct CallCounter(std::sync::Mutex<usize>);

    impl DecisionProvider for CallCounter {
        fn decide(
            &self,
            question: &Question,
            candidates: &[Candidate],
        ) -> Result<Decision, ProviderError> {
            *self.0.lock().unwrap() += 1;
            Ok(Decision {
                question_id: question.id.clone(),
                probabilities: candidates
                    .iter()
                    .map(|candidate| (candidate.id.clone(), 0.9))
                    .collect(),
            })
        }
    }

    #[test]
    fn single_over_limit_candidate_is_rejected_without_provider_call() {
        let provider = CallCounter::default();
        let candidate = Candidate {
            id: "a".into(),
            kind: CandidateKind::File,
            excerpt: "x".repeat(MAX_EXCERPT_BYTES + 1),
        };
        let outcome = decide_batched(&provider, &question(), &[candidate]);
        assert!(!outcome.complete);
        assert_eq!(outcome.error, Some(ProviderError::InvalidCandidates));
        assert_eq!(*provider.0.lock().unwrap(), 0);
    }

    #[test]
    fn candidate_bounds_and_duplicate_ids_are_refused() {
        let provider = FixtureProvider::from_json("{}").unwrap();
        assert_eq!(
            provider.decide(&question(), &[candidate("a"), candidate("a")]),
            Err(ProviderError::InvalidCandidates)
        );
        assert_eq!(
            provider.decide(
                &question(),
                &[Candidate {
                    id: "a".into(),
                    kind: CandidateKind::File,
                    excerpt: "x".repeat(MAX_EXCERPT_BYTES + 1),
                }],
            ),
            Err(ProviderError::InvalidCandidates)
        );
        let too_many_files: Vec<Candidate> = (0..=MAX_CANDIDATE_FILES)
            .map(|i| candidate(&format!("file-{i}")))
            .collect();
        assert_eq!(
            provider.decide(&question(), &too_many_files),
            Err(ProviderError::InvalidCandidates)
        );
        let too_many_declarations: Vec<Candidate> = (0..=MAX_DECLARATIONS)
            .map(|i| Candidate {
                id: format!("decl-{i}"),
                kind: CandidateKind::Declaration,
                excerpt: "x".into(),
            })
            .collect();
        assert_eq!(
            provider.decide(&question(), &too_many_declarations),
            Err(ProviderError::InvalidCandidates)
        );
        let oversized_batch: Vec<Candidate> = (0..9)
            .map(|i| Candidate {
                id: format!("batch-{i}"),
                kind: CandidateKind::File,
                excerpt: "x".repeat(7_300),
            })
            .collect();
        assert_eq!(
            provider.decide(&question(), &oversized_batch),
            Err(ProviderError::InvalidCandidates)
        );
        let oversized_json: Vec<Candidate> = (0..8)
            .map(|i| Candidate {
                id: format!("json-{i}"),
                kind: CandidateKind::File,
                excerpt: "\"".repeat(4_100),
            })
            .collect();
        assert_eq!(
            provider.decide(&question(), &oversized_json),
            Err(ProviderError::InvalidCandidates)
        );
    }
}

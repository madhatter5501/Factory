# Factory Process Gaps & Learnings

This document tracks process failures, gaps, and improvements to the factory agent workflows.

## Gap Log

### 2026-01-11: QA Agent Passed Ticket Despite Test Failures

**Ticket**: PRES-001-FIX-BUILD
**Issue**: QA agent passed a ticket even though `pnpm test` had 6 failing test files

**What happened**:
- Dev agent completed build fix
- QA agent ran tests, saw failures
- QA agent rationalized: "test failures appear to be configuration issues unrelated to the build integration problems"
- QA marked ticket as PASSED

**Root cause**:
- QA prompt did not explicitly state that ANY test failure should block the ticket
- QA agent used judgment to determine failures were "out of scope"

**Fix applied**:
1. Updated `prompts/qa.md` with explicit "Test Failure Policy" section
2. Added examples of wrong vs correct behavior
3. Updated `prompts/dev-frontend.md`, `dev-backend.md`, `dev-infra.md` to require tests pass before commit

**Lesson**: Agents will rationalize edge cases unless given explicit, unambiguous rules.

---

## Process Improvement Checklist

When adding new agent behaviors, verify:

- [ ] Edge cases are explicitly handled (not left to agent judgment)
- [ ] Failure conditions are enumerated with examples
- [ ] Success criteria are binary (pass/fail), not subjective
- [ ] Common mistakes are listed with "Do NOT" examples
- [ ] Prompt includes JSON examples of correct output format

## Future Improvements to Consider

1. **Pre-commit hooks for agents** - Automatically run tests before allowing status transitions
2. **Test result parsing** - Have orchestrator parse test output and block on failures
3. **Audit trail for decisions** - Log why agents made pass/fail decisions
4. **Regression testing for prompts** - Test prompts against known scenarios to catch regressions

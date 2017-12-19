# BuildKit Roadmap (tentative)

This document roughly describes the roadmap of the BuildKit project.
We will be using GitHub Projects and GitHub Milestones for more detailed roadmap.

## Task 1 (2018Q1)
- Implement all features needed for replacing the legacy builder backend of moby-engine

- Test, test, and test (help needed!)

- Integrate BuildKit to moby-engine as an experimental builder backend. (`moby-engine --experimental --build-driver buildkit`)
-- Probably for Linux only

## Task 2 (2018Q2-Q3)

- Promote BuildKit to the default moby-engine build backend
-- At this time, BuildKit API and LLB spec do not need to be stabilized

## Task 3 (2018Q1-Q2)

- Implement basic distributed mode

## Task 4 (2018-2019)

-- Stabilize BuildKit API and LLB spec

## Task 5 (2018-2019)
- Optimize distributed mode, especially on scalability of the distributed cache map (gossip protocol or something like that?)

## Task 6+
- Stabilize distributed mode
- Add more features

- - -

TODO: DAG-ify these roadmap tasks in more pretty format

<!-- e.g. PERT chart, but we should NOT be too much bureaucratic :P -->


```
1 ----->2
        |
        v
3 ----->4---+
 \          | 
  \         v
   +--->5-->6..
```

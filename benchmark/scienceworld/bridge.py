import json
import os
import sys


def emit(value):
    sys.stdout.write(json.dumps(value, separators=(",", ":"), ensure_ascii=False) + "\n")
    sys.stdout.flush()


try:
    import scienceworld.scienceworld as scienceworld_module
    from py4j.java_gateway import launch_gateway as py4j_launch_gateway

    gateway_output_sinks = []

    def quiet_launch_gateway(*args, **kwargs):
        # Py4J drains Java output on background threads, so these file handles
        # must remain open for the entire gateway lifetime.
        sink = open(os.devnull, "w")
        gateway_output_sinks.append(sink)
        kwargs.setdefault("redirect_stdout", sink)
        kwargs.setdefault("redirect_stderr", sink)
        return py4j_launch_gateway(*args, **kwargs)

    scienceworld_module.launch_gateway = quiet_launch_gateway
    from scienceworld import ScienceWorldEnv
except Exception as error:
    emit({"error": "ScienceWorld Python package could not be imported: " + str(error)})
    raise SystemExit(3)


environment = None
try:
    config = json.loads(sys.stdin.readline())
    environment = ScienceWorldEnv("", envStepLimit=int(config["step_limit"]))
    task_name = str(config["task_name"])
    variation = int(config["variation"])
    simplifications = str(config.get("simplifications", ""))
    environment.load(task_name, variation, simplifications)
    observation, info = environment.reset()
    emit({
        "task_name": info.get("taskName", task_name),
        "variation": info.get("variationIdx", variation),
        "score": info.get("score", 0),
        "observation": observation,
        "info": info,
        "valid_actions": environment.get_valid_action_object_combinations_with_templates(),
    })

    for line in sys.stdin:
        request = json.loads(line)
        if request.get("operation") == "close":
            emit({"closed": True})
            break
        if request.get("operation") != "step":
            emit({"error": "unsupported bridge operation"})
            continue
        observation, reward, completed, info = environment.step(str(request["action"]))
        emit({
            "observation": observation,
            "reward": reward,
            "completed": completed,
            "score": info.get("score"),
            "moves": info.get("moves"),
            "task_name": info.get("taskName"),
            "variation": info.get("variationIdx"),
            "simplifications": info.get("simplificationStr"),
            "goal_progress": environment.get_goal_progress(),
            "valid_actions": environment.get_valid_action_object_combinations_with_templates(),
        })
finally:
    if environment is not None:
        try:
            environment.close()
        except Exception:
            pass
    for sink in gateway_output_sinks:
        try:
            sink.close()
        except Exception:
            pass

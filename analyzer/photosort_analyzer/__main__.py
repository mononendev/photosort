"""python -m photosort_analyzer [--host 127.0.0.1] [--port 8090]"""
import argparse
import os

import uvicorn


def main():
    ap = argparse.ArgumentParser(prog="photosort-analyzer")
    ap.add_argument("--host", default=os.environ.get("ANALYZER_HOST", "127.0.0.1"))
    ap.add_argument("--port", type=int, default=int(os.environ.get("ANALYZER_PORT", "8090")))
    a = ap.parse_args()
    uvicorn.run("photosort_analyzer.server:app", host=a.host, port=a.port, log_level="warning", access_log=False)


if __name__ == "__main__":
    main()

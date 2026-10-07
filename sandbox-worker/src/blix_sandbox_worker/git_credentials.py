import os
import sys

if len(sys.argv) > 1 and sys.argv[1] == "get":
    print("username=x-access-token")
    print("password=" + os.environ.get("SANDBOX_GITHUB_TOKEN", ""))

FROM alpine:3.15

ADD ./systemReport /root/systemReport

# build
# docker build . -t ccr.ccs.tencentyun.com/oooo/system-report:latest
# push
# docker push ccr.ccs.tencentyun.com/oooo/system-report:latest

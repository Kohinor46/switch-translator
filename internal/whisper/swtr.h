#pragma once
#include "whisper.h"

void swtr_init(void);
int swtr_full(struct whisper_context * ctx, const float * samples, int n, const char * lang, int threads, int timestamps, int audio_ctx, void * handle);
